package httpapi

func (s *Server) routes() {

	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/summary", s.handleSummary)
	s.mux.HandleFunc("GET /v1/projects", s.handleProjects)
	s.mux.HandleFunc("GET /v1/activity", s.handleActivity)
	s.mux.HandleFunc("GET /v1/tools", s.handleTools)
	s.mux.HandleFunc("GET /v1/tool-calls", s.handleToolCalls)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /v1/mcps", s.handleMCPs)
	s.mux.HandleFunc("GET /v1/mcps/unused", s.handleUnusedMCPs)
	s.mux.HandleFunc("GET /v1/skills", s.handleSkills)
	s.mux.HandleFunc("GET /v1/skills/unused", s.handleUnusedSkills)

	s.mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	s.mux.HandleFunc("GET /v1/sessions/{id}", s.handleSession)
	s.mux.HandleFunc("GET /v1/sessions/{id}/handoff", s.handleSessionHandoff)
	s.mux.HandleFunc("GET /v1/turns", s.handleTurns)
	s.mux.HandleFunc("GET /v1/turns/{id}", s.handleTurn)
	s.mux.HandleFunc("GET /v1/turns/{id}/timeline", s.handleTurnTimeline)
	s.mux.HandleFunc("GET /v1/turns/{id}/components", s.handleTurnComponents)
	s.mux.HandleFunc("GET /v1/search", s.handleSearch)

	s.mux.HandleFunc("GET /v1/suggestions", s.handleSuggestions)
	s.mux.HandleFunc("POST /v1/suggestions:recompute", s.handleSuggestionsRecompute)
	s.mux.HandleFunc("POST /v1/export", s.handleExport)

	s.mux.HandleFunc("GET /v1/stream", s.handleStream)
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("POST /v1/events", s.handleEmit)
	s.mux.HandleFunc("POST /v1/hooks:intake", s.handleHooksIntake)

	s.mux.HandleFunc("GET /v1/daemon", s.handleDaemonStatus)
	s.mux.HandleFunc("POST /v1/daemon:trigger", s.handleDaemonIngest)
	s.mux.HandleFunc("POST /v1/daemon:pause", s.handleDaemonPause)
	s.mux.HandleFunc("POST /v1/daemon:resume", s.handleDaemonResume)

	s.mux.HandleFunc("GET /v1/sources", s.handleSources)
	s.mux.HandleFunc("GET /v1/config", s.handleConfig)
	s.mux.HandleFunc("POST /v1/config:reload", s.handleConfigReload)
	s.mux.HandleFunc("POST /v1/config:set", s.handleConfigSet)

	s.mux.HandleFunc("POST /v1/data:prune", s.handleDataPruneRaw)
	s.mux.HandleFunc("GET /v1/data/retention", s.handleRetentionStatus)
	s.mux.HandleFunc("GET /v1/data/retention/profiles", s.handleRetentionProfiles)
	s.mux.HandleFunc("POST /v1/data/retention:prune", s.handleRetentionPrune)
}
