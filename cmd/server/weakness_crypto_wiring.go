// weakness_crypto_wiring.go — composition root for WS-5 (ADR-205 D8 / CHO-1957):
// the per-blob envelope crypto-shred + the source_material (textbook) consent gate.
//
// DARK by default. The whole envelope/shred lifecycle is gated by
// WEAKNESS_BLOB_ENVELOPE_ENABLED (default off), coupled to the graduated-crew
// cutover (the crew must be able to DECRYPT the blob during analysis) + the
// master KEK provisioning. The source_material consent gate is gated separately
// by WEAKNESS_SOURCE_MATERIAL_ENABLED.
//
// fail-loud: with the envelope ENABLED the service refuses to boot unless a real
// KEK is wired — it never silently stores plaintext.
//
// Per feedback_no_inline_config every env read happens here at the composition
// root; the handler + subscriber + domain never touch the environment.
package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/cryptokms"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

const defaultBlobShredTTLHours = 24

func weaknessEnvelopeEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("WEAKNESS_BLOB_ENVELOPE_ENABLED")), "true")
}

// wireWeaknessBlobCryptoShred wires the source_material consent gate (always) +
// the per-blob envelope encrypt-on-upload (when enabled). MUST run after
// wireGrowthEdgeUploads (ext.WeaknessBlobs). The shred-on-analyse side is wired
// onto the subscriber by maybeWithBlobShredHook at subscriber-build time.
func wireWeaknessBlobCryptoShred(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	smEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv("WEAKNESS_SOURCE_MATERIAL_ENABLED")), "true")
	ext.SourceMaterial = wu.NewSourceMaterialGate(smEnabled)
	log.Printf("consumption: source_material (textbook) upload gate enabled=%v (WEAKNESS_SOURCE_MATERIAL_ENABLED) [WS-5]", smEnabled)

	if !weaknessEnvelopeEnabled() {
		log.Printf("consumption: blob envelope crypto-shred DISABLED (DARK) — plaintext upload, no shred [WS-5; couples to crew cutover + KEK provisioning]")
		return
	}
	// ENABLED — every dependency is mandatory; fail-loud at boot (never accept a
	// premium upload + silently store it unencrypted because a dep was missing).
	if pool == nil {
		log.Fatalf("consumption: WEAKNESS_BLOB_ENVELOPE_ENABLED=true but no pgx pool for the wrapped-DEK store. Refusing to boot.")
	}
	kekEnv := strings.TrimSpace(os.Getenv("WEAKNESS_BLOB_KEK_ENV"))
	if kekEnv == "" {
		kekEnv = "CHORA_BLOB_KEK"
	}
	kek, err := newBlobKEKClient(kekEnv)
	if err != nil {
		log.Fatalf("consumption: blob KEK client unavailable: %v. Refusing to boot (fail-loud; never store plaintext).", err)
	}
	ext.BlobEnvelope = wb.NewEnvelope(kek)
	ext.BlobDEKs = pg.NewWeaknessBlobDEKWrapRepo(pg.NewPgxTxRunner(pool))
	log.Printf("consumption: blob envelope crypto-shred ENABLED (WS-5) — per-blob DEK wrapped by KEK env=%s", kekEnv)
}

// maybeWithBlobShredHook attaches the WS-5 crypto-shred hook to the analyzed
// subscriber when the envelope is enabled: a completed diagnosis tombstones the
// wrapped DEK (the crypto-shred guarantee) + deletes the GCS ciphertext + records
// the audit marker + runs a best-effort per-tenant TTL sweep (the backstop for
// blobs whose diagnosis never completed). DARK by default ⇒ returns sub unchanged.
//
// NB: the shred path needs NO KEKClient (it only deletes the wrapped DEK, never
// decrypts) — but it is gated by the SAME flag because there is nothing to shred
// until the encrypt path is on. When the envelope is enabled the encrypt wiring
// (wireWeaknessBlobCryptoShred) fail-loud-requires a KEK, so in practice both
// activate together at cutover.
func maybeWithBlobShredHook(sub *subscribers.WeaknessAnalyzedSubscriber, pool *pgxpool.Pool) *subscribers.WeaknessAnalyzedSubscriber {
	if !weaknessEnvelopeEnabled() || pool == nil {
		return sub
	}
	store := pg.NewWeaknessBlobDEKWrapRepo(pg.NewPgxTxRunner(pool))
	uploadRepo := pg.NewWeaknessUploadRepo(pg.NewPgxTxRunner(pool))

	var deleter wb.BlobDeleter
	bucket := strings.TrimSpace(os.Getenv("WEAKNESS_UPLOADS_BUCKET"))
	if bs, err := storage.NewWeaknessBlobStore(storage.WeaknessBlobStoreConfig{Bucket: bucket}); err == nil {
		deleter = bs // *WeaknessBlobStore satisfies wb.BlobDeleter
	} else {
		log.Printf("consumption: weakness blob deleter NOT wired (%v) — crypto-shred will tombstone the DEK only (ciphertext relies on the GCS object lifecycle TTL)", err)
	}

	shredder := wb.NewShredder(store, deleter)
	ttl := blobShredTTL()
	sweeper := wb.NewSweeper(store, shredder, ttl)

	hook := func(ctx context.Context, tenantID, gcid, uploadID string) error {
		// 1+2. Crypto-shred this blob (tombstone DEK + delete ciphertext) — fail-loud.
		if err := shredder.Shred(ctx, tenantID, gcid, uploadID); err != nil {
			return err
		}
		// 3. Audit marker on the job row — fail-loud (idempotent).
		if err := uploadRepo.MarkBlobShredded(ctx, gcid, uploadID, time.Now().UTC()); err != nil {
			return err
		}
		// 4. Best-effort TTL backstop for THIS tenant's stuck blobs (RLS-scoped,
		// no cross-tenant scan). Never fails the analysis — the per-blob shred
		// above is the guarantee; the dedicated scheduler is a flagged follow-up.
		if n, err := sweeper.SweepExpired(ctx); err != nil {
			log.Printf("consumption: weakness blob TTL sweep (tenant=%s) error (non-fatal): %v", tenantID, err)
		} else if n > 0 {
			log.Printf("consumption: weakness blob TTL sweep shredded %d expired blob(s) (tenant=%s)", n, tenantID)
		}
		return nil
	}
	log.Printf("consumption: weakness blob crypto-shred-on-analyse hook wired (TTL backstop=%s) [WS-5]", ttl)
	return sub.WithBlobShredHook(hook)
}

func blobShredTTL() time.Duration {
	hours := defaultBlobShredTTLHours
	if v := strings.TrimSpace(os.Getenv("WEAKNESS_BLOB_SHRED_TTL_HOURS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	return time.Duration(hours) * time.Hour
}

// newBlobKEKClient builds the production KEKClient for per-blob DEK wrapping
// (CHO-1957 WS-5). The master KEK is env-provided (base64 32-byte AES-256 key,
// the cloud-neutral replacement for Cloud KMS); fails LOUD on any resolution
// error so WEAKNESS_BLOB_ENVELOPE_ENABLED=true never boots into a silent
// plaintext store.
//
// Remaining cutover deps are owner-gated:
//   - the base64 KEK provisioned into the deployment env (WEAKNESS_BLOB_KEK_ENV
//     names the var; default CHORA_BLOB_KEK)
//   - WEAKNESS_BLOB_ENVELOPE_ENABLED flipped to "true"
func newBlobKEKClient(resource string) (wb.KEKClient, error) {
	return cryptokms.NewBlobKEK(context.Background(), resource)
}
