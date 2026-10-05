// knowledge_graph_handler.go — EXT scope KG read endpoints.
//
// Endpoints:
//
//	GET /knowledge-graph/discover?topic=X&depth=N
//	GET /me/knowledge-graph/heatmap
//
// Discover delegates BFS to domain/knowledge_graph_read.Discover, which
// is wired against an injected Graph adapter (production: HTTP client to
// chora-creation; tests: in-memory stub).
//
// The heatmap endpoint reads the learner's per-topic SM-2 retention
// scores and converts them to RAG (red / amber / green) tiers. MVP mock
// returns an empty heatmap — real wire-up to chora_consumption SM-2
// retention reads lands in M12 alongside the AtomProjection cache.
package http

import (
	"net/http"
	"strconv"

	"github.com/apollo-chora/chora-consumption/internal/domain/knowledge_graph_read"
)

type extDiscoverResp struct {
	Seed   string   `json:"seed"`
	Depth  int      `json:"depth"`
	Topics []string `json:"topics"`
}

type extHeatmapCellResp struct {
	Topic     string  `json:"topic"`
	Retention float32 `json:"retention"`
	Tier      string  `json:"tier"`
}

type extHeatmapResp struct {
	GCID  string               `json:"gcid"`
	Cells []extHeatmapCellResp `json:"cells"`
}

func (s *ExtServer) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	if s.Graph == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GRAPH_NOT_WIRED",
			"knowledge-graph adapter not configured")
		return
	}
	topic := r.URL.Query().Get("topic")
	if topic == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_TOPIC", "topic query param required")
		return
	}
	depthStr := r.URL.Query().Get("depth")
	depth := 1
	if depthStr != "" {
		var err error
		depth, err = strconv.Atoi(depthStr)
		if err != nil {
			extWriteError(w, http.StatusBadRequest, "INVALID_DEPTH", err.Error())
			return
		}
	}
	res, err := knowledge_graph_read.Discover(s.Graph, topic, depth)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "DISCOVER_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, extDiscoverResp{
		Seed:   res.Seed,
		Depth:  res.Depth,
		Topics: res.Topics,
	})
}

func (s *ExtServer) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	_, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	// MVP: empty heatmap. Real per-gcid retention lookup lands in M12 with
	// the SM-2 retention projection (chora_consumption.spaced_repetition).
	hm := knowledge_graph_read.BuildHeatmap(gcid, map[string]float32{})
	cells := make([]extHeatmapCellResp, 0, len(hm.Cells))
	for _, c := range hm.Cells {
		cells = append(cells, extHeatmapCellResp{
			Topic:     c.Topic,
			Retention: c.Retention,
			Tier:      string(c.Tier),
		})
	}
	extWriteJSON(w, http.StatusOK, extHeatmapResp{
		GCID:  hm.GCID,
		Cells: cells,
	})
}
