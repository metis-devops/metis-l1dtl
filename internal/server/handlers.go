package server

import "net/http"

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]bool{"alive": true})
}

func (s *Server) readiness(w http.ResponseWriter, _ *http.Request) {
	ready, err := s.Status.Snapshot()
	if !ready || err != nil {
		reply(w, http.StatusServiceUnavailable, map[string]bool{"ready": false})
		return
	}
	reply(w, http.StatusOK, map[string]bool{"ready": true})
}

func (s *Server) enqueue(_ *http.Request, index *uint64) (any, int, error) {
	deposit, err := s.Store.Get(index)
	if err != nil {
		s.Status.Set(false, err)
		return nil, http.StatusServiceUnavailable, err
	}
	if deposit != nil {
		return deposit, http.StatusOK, nil
	}
	return map[string]any{
		"index":       nil,
		"target":      nil,
		"data":        nil,
		"gasLimit":    nil,
		"origin":      nil,
		"blockNumber": nil,
		"timestamp":   nil,
		"ctcIndex":    nil,
	}, http.StatusOK, nil
}

func (s *Server) highestL1(_ *http.Request, _ *uint64) (any, int, error) {
	state, err := s.Store.State()
	if err != nil {
		s.Status.Set(false, err)
		return nil, http.StatusServiceUnavailable, err
	}
	return map[string]any{"blockNumber": state.Height}, http.StatusOK, nil
}

func (s *Server) syncing(_ *http.Request, _ *uint64) (any, int, error) {
	ready, _ := s.Status.Snapshot()
	return map[string]any{"syncing": !ready, "currentTransactionIndex": 0}, http.StatusOK, nil
}

func (s *Server) transaction(_ *http.Request, _ *uint64) (any, int, error) {
	return map[string]any{"transaction": nil, "batch": nil}, http.StatusOK, nil
}

func (s *Server) block(_ *http.Request, _ *uint64) (any, int, error) {
	return map[string]any{"block": nil, "batch": nil}, http.StatusOK, nil
}
