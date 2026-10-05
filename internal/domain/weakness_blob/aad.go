package weakness_blob

// BlobAAD is the Additional Authenticated Data binding a wrapped DEK + its
// ciphertext to one blob's context. Binding tenant + gcid + upload means a
// wrapped DEK can only be unwrapped for the exact (tenant, learner, upload) it
// was sealed for — cross-tenant / cross-learner / cross-blob reuse fails closed.
// Stable byte layout (never reorder — changing it strands existing ciphertext).
func BlobAAD(tenantID, gcid, uploadID string) []byte {
	return []byte("weakness-blob:tenant=" + tenantID + ";gcid=" + gcid + ";upload=" + uploadID)
}
