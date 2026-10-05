// growth_wiring.go — bootstrap helpers for the ADR-149 Companion Growth
// axis (Iter G.5 PROD-B).
//
// Wires:
//   - growth.Service (domain) backed by pg.GrowthRepo + outbox.GrowthOutbox
//   - clients.BreedDistributionClient (REST → chora-tenancy odds endpoint)
//     with StaticBreedDistributionProvider fallback when chora-tenancy is
//     not configured.
//   - subscribers.ProvisionEggSubscriber + CompanionGrowthSubscriber wiring
//     hooks (subscribers' Pub/Sub binding lives in the existing
//     subscriber bootstrap, not in this file).
//
// Per feedback_no_inline_config + secrets-and-env: env contract:
//
//	CHORA_TENANCY_BASE_URL            chora-tenancy base URL for odds REST
//	CHORA_TENANCY_EGG_ODDS_CACHE_TTL  cache TTL Go duration (default 5m)
//
// When CHORA_TENANCY_BASE_URL is empty (dev), falls back to a
// StaticBreedDistributionProvider seeded with the canonical
// "egg.standard.v1" distribution so HatchEgg works against test SKUs.
package main

import (
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"errors"
	"github.com/apollo-chora/chora-common/eventbus"

	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"


	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// hatchExpThresholdEnv names the incubation hatch-threshold override.
const hatchExpThresholdEnv = "COMPANION_HATCH_EXP_THRESHOLD"

// parseHatchExpThreshold resolves COMPANION_HATCH_EXP_THRESHOLD (F-I1.2,
// ADR-228 incubation). Empty ⇒ growth.DefaultHatchExpThreshold. A non-empty
// value MUST parse and satisfy 0 < t < 50 — the ADR-149 Stage-2 curve
// boundary is 50, so an egg must stir well before a post-hatch Baby would
// itself become curve-eligible. Anything else is a fail-loud boot error
// (per feedback_no_inline_config: a misconfigured threshold must never
// silently clamp).
func parseHatchExpThreshold(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return growth.DefaultHatchExpThreshold, nil
	}
	t, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not an integer: %w", hatchExpThresholdEnv, raw, err)
	}
	if t <= 0 || t >= 50 {
		return 0, fmt.Errorf("%s=%d out of range — must satisfy 0 < t < 50", hatchExpThresholdEnv, t)
	}
	return t, nil
}

// bootstrapBreedDistributionProvider builds the breed_distribution provider
// that growth.Service injects into HatchEgg + PreviewEggOdds.
//
// Resolution order (#12 Iter G.4 PROD-C):
//  1. CHORA_TENANCY_GRPC_BASE_URL set → typed gRPC client (mesh DNS,
//     e.g. "chora-tenancy:9090"). Faster + first-class mesh mTLS.
//  2. CHORA_TENANCY_BASE_URL set    → live REST client with 5-min cache
//     (PROD-B path; retained for backward-compat + cross-cluster ingress).
//  3. Otherwise → static fallback (dev only).
//
// Per ddd-enforcement HARD RULE: this is the sanctioned cross-domain
// read path for breed_distribution; chora-consumption MUST NOT query
// chora_tenancy DB directly.
func bootstrapBreedDistributionProvider() growth.BreedDistributionProvider {
	ttl := clients.DefaultBreedDistributionCacheTTL
	if raw := os.Getenv("CHORA_TENANCY_EGG_ODDS_CACHE_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			ttl = d
		} else {
			log.Printf("consumption: CHORA_TENANCY_EGG_ODDS_CACHE_TTL invalid (%v); using default %v", err, ttl)
		}
	}

	// 1. Prefer gRPC when configured.
	if grpcTarget := strings.TrimSpace(os.Getenv("CHORA_TENANCY_GRPC_BASE_URL")); grpcTarget != "" {
		c, err := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
			Target:      grpcTarget,
			CacheTTL:    ttl,
			CallTimeout: 5 * time.Second,
		})
		if err != nil {
			log.Printf("consumption: NewBreedDistributionGRPCClient(%s) failed: %v — falling through to REST", grpcTarget, err)
		} else {
			log.Printf("consumption: BreedDistributionGRPCClient wired (target=%s, cache_ttl=%v)", grpcTarget, ttl)
			return c
		}
	}

	// 2. Fall back to REST.
	if baseURL := strings.TrimSpace(os.Getenv("CHORA_TENANCY_BASE_URL")); baseURL != "" {
		log.Printf("consumption: BreedDistributionClient (REST) wired (base=%s, cache_ttl=%v)", baseURL, ttl)
		return clients.NewBreedDistributionClient(clients.BreedDistributionClientOptions{
			BaseURL:  baseURL,
			Timeout:  5 * time.Second,
			CacheTTL: ttl,
		})
	}

	// 3. Static fallback.
	log.Printf("consumption: CHORA_TENANCY_GRPC_BASE_URL + CHORA_TENANCY_BASE_URL both unset — using StaticBreedDistributionProvider (dev only)")
	return clients.NewStaticBreedDistributionProvider(nil)
}

// wireGrowthService builds the growth.Service against the supplied pgx pool +
// outbox publisher, then attaches it to the existing http.Server. When pool
// or publisher are nil, the function logs and returns without wiring
// (Server.Growth stays nil → REST handlers fall back to 503).
//
// Returns:
//   - growthService for use by subscribers (nil when not wired)
func wireGrowthService(
	srv *httpadapter.Server,
	pool *pgxpool.Pool,
	publisher *consumptionoutbox.Publisher,
) *growth.Service {
	if pool == nil {
		log.Printf("consumption: growth axis NOT wired (no pgx pool); /v1/me/companions/{id}/growth* returns 503")
		return nil
	}
	if publisher == nil {
		log.Printf("consumption: growth axis NOT wired (no outbox publisher); /v1/me/companions/{id}/growth* returns 503")
		return nil
	}

	txRunner := pg.NewPgxTxRunner(pool)
	repo := pg.NewGrowthRepo(txRunner)

	// The growth outbox is per-domain — use a dedicated Publisher with
	// AggregateType="companion" so audit + replay queries on aggregate_id
	// stay meaningful.
	growthPublisher := publisher
	if publisher != nil {
		// Reuse the same Store but mint a Publisher with the companion
		// aggregate label. We can't easily clone publisher's cfg without
		// importing internals — for now reuse the existing publisher.
		// (Audit aggregate_id is overridden by payload "companion_id" key
		// in publisher.Publish.)
		growthPublisher = publisher
	}
	gOutbox := consumptionoutbox.NewGrowthOutbox(growthPublisher)

	// Shared with the effective-odds handler: one provider, one cache, one
	// declared table. Two instances could serve different tables for a TTL
	// window after a SKU edit, which would put the published odds and the roll
	// out of step (see companion_egg_odds_wiring.go).
	dist := sharedBreedDistributionProvider()

	// CHO-2012 (ADR-218 D3): the loadout surfaces — pg-backed catalogue /
	// species paths / grants bridge + the durable loadout outbox — feed the
	// species-Path unlocker AND replace the http server's in-memory loadout
	// defaults so REST + gRPC + subscriber paths share one store.
	loadoutRepo := pg.NewCompanionLoadoutRepo(txRunner)
	loadoutOutbox := consumptionoutbox.NewLoadoutOutbox(growthPublisher)
	srv.Loadouts = loadoutRepo
	srv.SkillCatalog = loadoutRepo
	srv.LoadoutEvents = loadoutOutbox
	srv.SpeciesPaths = loadoutRepo
	unlocker, uerr := companion.NewPathUnlocker(companion.PathUnlockerConfig{
		// CHO-2225: the envelope event_id must be a UUIDv7 on
		// companion.skill_granted.v1 / companion.loadout_changed.v1 too.
		NewID:   domain.NewUUIDv7,
		Catalog: loadoutRepo,
		Paths:   loadoutRepo,
		Grants:  loadoutRepo,
		Outbox:  loadoutOutbox,
	})
	if uerr != nil {
		log.Printf("consumption: path unlocker NOT wired: %v — growth axis not wired", uerr)
		return nil
	}

	// CHO-2012 (ADR-218 D6): resolve EXP rules via identity's
	// ExpRuleService when configured; otherwise the growth service uses the
	// documented in-code parity fallback (behaviour-identical by the 0029
	// parity seeds). A resolver FAILURE at award time logs loudly via
	// OnExpRuleFallback — degradation is never silent.
	var expRuler growth.ExpRuler
	if target := strings.TrimSpace(os.Getenv("CHORA_IDENTITY_EXP_RULES_GRPC_URL")); target != "" {
		erc, cerr := clients.NewExpRuleClient(target, 0)
		if cerr != nil {
			log.Fatalf("consumption: exp-rule client dial %q failed: %v — fail-loud per feedback_no_inline_config", target, cerr)
		}
		expRuler = erc
		log.Printf("consumption: EXP rules resolved via identity ExpRuleService @ %s (ADR-218 D6)", target)
	} else {
		log.Printf("consumption: CHORA_IDENTITY_EXP_RULES_GRPC_URL unset — EXP rules use the in-code parity fallback (ADR-218 D6 documented fallback)")
	}

	// F-I1.2 (ADR-228): resolve the incubation hatch threshold at boot.
	// fail-loud on a malformed / out-of-range override (never silently clamp).
	hatchThreshold, herr := parseHatchExpThreshold(os.Getenv(hatchExpThresholdEnv))
	if herr != nil {
		log.Fatalf("consumption: %v — fail-loud per feedback_no_inline_config", herr)
	}

	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: gOutbox,
		Dist:   dist,
		// CHO-2225: the envelope event_id must be a UUIDv7. The growth domain
		// does not import google/uuid, so the mint is injected here — as the
		// sibling companion_binding_wiring.go already does.
		NewID:             domain.NewUUIDv7,
		ExpRules:          expRuler,
		HatchExpThreshold: hatchThreshold,
		OnExpRuleFallback: func(source string, ferr error) {
			log.Printf("consumption: LOUD FALLBACK — exp-rule resolution failed for source %q (award proceeds on in-code parity values): %v [ADR-218 D6]", source, ferr)
		},
		SkillUnlocker: pathUnlockerAdapter{unlocker},
		// CHO-2013 P1 (R3-1): the awakening resonant-concept pick needs the
		// goal binding + concept existence ports; both ride the same pg tx
		// runner (same chora_consumption DB, RLS-scoped).
		GoalBinding: goalBindingAdapter{goals: pg.NewGoalRepo(txRunner)},
		Concepts:    conceptCheckerAdapter{concepts: pg.NewConceptNodeRepo(txRunner)},
	})
	if err != nil {
		log.Printf("consumption: growth.NewService failed: %v — growth axis not wired", err)
		return nil
	}
	srv.Growth = httpadapter.NewGrowthServiceAdapter(svc)
	// D3: the ROSTER read warms a Stage-0 pod towards this same gate. Set from
	// the one resolved value above so /me/companions and /me/companions/{id}/
	// growth can never answer with different denominators.
	srv.HatchExpThreshold = hatchThreshold
	log.Printf("consumption: growth axis WIRED (pg + outbox + breed dist + loadout/path-unlocker + exp-rules=%v + hatch_threshold=%d)", expRuler != nil, hatchThreshold)
	return svc
}

// goalBindingAdapter satisfies growth.GoalBinding over the pg goal repo:
// finds the Goal owning AttachedCompanionID == companionID, projected to the
// narrow growth.AttachedGoal shape (CHO-2013 P1, R3-1).
type goalBindingAdapter struct {
	goals *pg.GoalRepo
}

func (a goalBindingAdapter) FindByAttachedCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) (*growth.AttachedGoal, error) {
	gs, err := a.goals.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		if g == nil || g.AttachedCompanionID == nil || *g.AttachedCompanionID != companionID {
			continue
		}
		out := &growth.AttachedGoal{GoalID: g.GoalID, Title: g.NorthStarNote}
		if g.RootConceptID != nil {
			out.RootConceptID = *g.RootConceptID
		}
		return out, nil
	}
	return nil, nil
}

// conceptCheckerAdapter satisfies growth.ConceptChecker over the pg
// concept-node repo (RLS-scoped learner ownership built in).
type conceptCheckerAdapter struct {
	concepts *pg.ConceptNodeRepo
}

func (a conceptCheckerAdapter) Exists(ctx context.Context, tenantID, learnerGCID, conceptID string) (bool, error) {
	c, err := a.concepts.GetByID(ctx, tenantID, learnerGCID, conceptID)
	if err != nil {
		return false, err
	}
	return c != nil, nil
}

// pathUnlockerAdapter bridges companion.PathUnlocker to the two-phase
// growth.SkillUnlocker port (CHO-2039: MintThroughStage joins the award
// transaction via the pg ambient-Querier ctx — both repos share ONE
// TxRunner — while PublishGranted stays post-commit).
type pathUnlockerAdapter struct {
	u *companion.PathUnlocker
}

func (a pathUnlockerAdapter) MintThroughStage(ctx context.Context, in growth.UnlockThroughStageInput) ([]growth.MintedPathGrant, error) {
	minted, err := a.u.MintThroughStage(ctx, toCompanionUnlockInput(in))
	if err != nil {
		return nil, err
	}
	out := make([]growth.MintedPathGrant, 0, len(minted))
	for _, m := range minted {
		out = append(out, growth.MintedPathGrant{
			SkillKey:        m.SkillKey,
			SkillKind:       string(m.SkillKind),
			Equipped:        m.Equipped,
			UnlockedVia:     m.UnlockedVia,
			UnlockedAtStage: m.UnlockedAtStage,
		})
	}
	return out, nil
}

func (a pathUnlockerAdapter) PublishGranted(ctx context.Context, in growth.UnlockThroughStageInput, minted []growth.MintedPathGrant) error {
	fm := make([]companion.MintedGrant, 0, len(minted))
	for _, m := range minted {
		fm = append(fm, companion.MintedGrant{
			SkillKey:        m.SkillKey,
			SkillKind:       companion.SkillKind(m.SkillKind),
			Equipped:        m.Equipped,
			UnlockedVia:     m.UnlockedVia,
			UnlockedAtStage: m.UnlockedAtStage,
		})
	}
	return a.u.PublishGranted(ctx, toCompanionUnlockInput(in), fm)
}

func toCompanionUnlockInput(in growth.UnlockThroughStageInput) companion.UnlockInput {
	return companion.UnlockInput{
		TenantID:    in.TenantID,
		CompanionID: in.CompanionID,
		OwnerGCID:   in.OwnerGCID,
		Species:     in.Species,
		Stage:       in.Stage,
		Traceparent: in.Traceparent,
		Tracestate:  in.Tracestate,
	}
}

// wireEggPurchaseSubscriber builds the ProvisionEggSubscriber that consumes
// chora.payments.companion_egg_purchase.payment_captured.v1 DIRECTLY (mana-pattern
// parity — no chora-tenancy relay). Returns nil when the growth service is not
// wired (no point subscribing without somewhere to provision).
//
// The actual Pub/Sub subscription handler binding is done by the existing
// subscriber bootstrap path (S6.4 + W1.5b). This helper exists so other
// agents can locate the subscriber wiring entry point.
func wireEggPurchaseSubscriber(svc *growth.Service) *subscribers.ProvisionEggSubscriber {
	if svc == nil {
		return nil
	}
	return subscribers.NewProvisionEggSubscriber(svc)
}

// wireCompanionGrowthSubscriber builds the 6-source EXP awarder subscriber.
// resolver is supplied by the caller (typically the active-Companion
// dispatch service; for build-out a simple "first non-deleted" lookup is OK).
func wireCompanionGrowthSubscriber(svc *growth.Service, resolver subscribers.CompanionResolver) *subscribers.CompanionGrowthSubscriber {
	if svc == nil || resolver == nil {
		return nil
	}
	return subscribers.NewCompanionGrowthSubscriber(svc, resolver)
}

// nopCompanionResolver is the defensive fallback CompanionResolver that always
// returns ErrNoCompanion — the subscriber treats that as a non-fatal drop.
// Retained only for the (abnormal) case where the server has no
// CompanionInstances repo wired; the normal path uses pickCompanionResolver →
// RepoCompanionResolver.
type nopCompanionResolver struct{}

func (nopCompanionResolver) ResolveActiveCompanion(_ context.Context, _, _ string) (string, error) {
	return "", subscribers.ErrNoCompanion
}

// pickCompanionResolver selects the CompanionResolver for the growth
// subscriber. When the server has a CompanionInstances repo wired (always, in
// prod + dev — NewServer defaults it to the in-memory adapter, and
// wireCompanionInstanceRepo swaps the pg-backed one when a pool exists) it
// returns the repo-backed RepoCompanionResolver. Otherwise it falls back to
// nopCompanionResolver.
//
// CHO-2012 (ADR-218 D7): the resolver now attributes through Goal
// attachment — `goals` is the learner-goal read slice (ext.Goals; nil keeps
// the legacy oldest-roster-only behaviour so EXP still flows when the
// goals repo is not wired).
//
// F1 (ADR-149 / handoff 2026-06-01): this replaces the previously-hardwired
// nopCompanionResolver{}, which silently dropped ALL 7-source EXP awards.
func pickCompanionResolver(srv *httpadapter.Server, goals subscribers.GoalAttachmentLister) subscribers.CompanionResolver {
	if srv != nil && srv.CompanionInstances != nil {
		return subscribers.NewRepoCompanionResolver(srv.CompanionInstances, goals)
	}
	return nopCompanionResolver{}
}

// wireGrowthPushHandlers builds the CompanionGrowthSubscriber +
// ProvisionEggSubscriber Go structs AND the HTTP push-handler that fronts
// each, attaching them to the http.Server so /internal/pubsub/companion-
// growth-source-events + /internal/pubsub/egg-purchase-provision land
// registered on Routes(). Per ADR-149 / Iter G.8 / m14.iter5g.code-p (#11).
//
// Per the always-loaded secrets-and-env rule the OIDC verifier is wired
// from env:
//
//	CHORA_PUBSUB_PUSH_AUDIENCE — the public push URL the subscription was
//	  created with (Cloud Run service URL; per-environment). When empty,
//	  OIDC verification is DISABLED (dev / in-cluster mTLS path only).
//	CHORA_PUBSUB_PUSH_TRUSTED_SA — comma-separated SA emails the Pub/Sub
//	  OIDC token may claim. When empty, any verified Google-issued SA token
//	  passes the audience check.
//
// Both subscriber structs are constructed here so the HTTP handler has a
// concrete target. The CompanionResolver is chosen by pickCompanionResolver —
// the repo-backed RepoCompanionResolver when srv.CompanionInstances is wired
// (the normal path), else the nopCompanionResolver fallback.
// derived is the W3-derived weakness projector fanned into the
// atom_session.completed dispatch (nil leaves growth-EXP-only behaviour).
func wireGrowthPushHandlers(ctx context.Context, srv *httpadapter.Server, growthSvc *growth.Service, derived *subscribers.DerivedWeaknessProjector, goals subscribers.GoalAttachmentLister, bus eventbus.Bus) {
	if growthSvc == nil {
		return
	}
	if bus == nil {
		log.Printf("consumption: growth push handlers NOT wired (no event bus, NATS_URL unset)")
		return
	}

	resolver := pickCompanionResolver(srv, goals)
	companionSub := wireCompanionGrowthSubscriber(growthSvc, resolver)
	if companionSub != nil {
		go func() {
			log.Printf("consumption: companion-growth subscriber binding the EXP source topics (resolver=%T, goal-attributed=%v)", resolver, goals != nil)
			handler := subscribers.CompanionGrowthHandler(companionSub, derived)
			for _, subject := range []string{
				events.TopicAtomSessionCompleted,
				"chora.consumption.kg.hexagon_expanded.v1",
				"chora.consumption.kg.junction_accepted.v1",
				events.TopicDailyDoseServed,
				subscribers.TopicSharingPostCreated,
				subscribers.TopicSharingReactionAdded,
				subscribers.TopicCreationAtomPublished,
			} {
				if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.companion-growth", subject), handler); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("ERROR consumption: companion-growth subscriber exited: %v", err)
				}
			}
		}()
	}
	eggSub := wireEggPurchaseSubscriber(growthSvc)
	if eggSub != nil {
		go func() {
			log.Printf("consumption: egg-purchase subscriber binding the payments egg-capture topics")
			handler := subscribers.EggPurchaseHandler(eggSub)
			for _, subject := range []string{subscribers.TopicPaymentsCompanionEggCaptured, subscribers.TopicPaymentsFamiliarEggCapturedLegacy} {
				if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.egg-purchase", subject), handler); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("ERROR consumption: egg-purchase subscriber exited: %v", err)
				}
			}
		}()
	}
}


// splitCSV trims + filters a comma-separated env-var into a slice.
func splitCSV(s string) []string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
