// Package grpc — KnowledgeGraphService gRPC adapter (Wave-1 CN-FULL,
// 2026-05-16, docs/m13/grpc-mass-remediation-2026-05-16.md).
//
// Registers the typed chora.services.consumption.v1.KnowledgeGraphService
// gRPC surface. Per ADR-143, the per-user Knowledge Graph is owned by
// chora-consumption (NOT chora-creation) — MapCluster / Exploration /
// HexagonNode / TrailHop aggregates all live in chora_consumption DB.
//
// Today the REST surface (chora-consumption/internal/adapter/http/
// knowledge_graph_handler.go) fronts these aggregates; this gRPC
// server is the Wave-1 contract registration so chora-gateway BFF
// (Wave-2 GW-SWITCH) and peer services can cut over to typed gRPC.
//
// Per the always-loaded `feedback_no_stubs_real_wiring`: NO conditional
// registration. RPCs return `codes.Unimplemented` (via the embedded
// UnimplementedKnowledgeGraphServiceServer) until each is wired RPC-by-
// RPC to the user_knowledge_graph domain package + pg repos. This is
// the canonical fail-loud signal — NOT a silent stub.
package grpc

import (
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// KnowledgeGraphServer is the gRPC adapter for the KnowledgeGraphService.
type KnowledgeGraphServer struct {
	consumptionv1.UnimplementedKnowledgeGraphServiceServer
}

// NewKnowledgeGraphServer constructs the server.
func NewKnowledgeGraphServer() *KnowledgeGraphServer {
	return &KnowledgeGraphServer{}
}
