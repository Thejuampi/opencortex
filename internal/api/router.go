package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"opencortex/internal/api/handlers"
	apimw "opencortex/internal/api/middleware"
	ws "opencortex/internal/api/websocket"
	"opencortex/internal/service"
)

func NewRouter(server *handlers.Server, app *service.App, hub *ws.Hub) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(apimw.Logging)

	r.Get("/healthz", server.Health)

	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/ws", hub.ServeWS)
		api.Get("/admin/health", server.AdminHealth)
		// Auto-registration: localhost-only, no API key required.
		// Loopback enforcement is done inside the handler.
		api.Post("/agents/auto-register", server.AutoRegister)

		api.Group(func(protected chi.Router) {
			protected.Use(apimw.RequireAuth(app))
			protected.Use(apimw.NewRateLimiter(1000, 200).Middleware)

			// Agents
			protected.With(apimw.RequirePermission(app, "agents", "write")).Post("/agents", server.CreateAgent)
			protected.With(apimw.RequirePermission(app, "agents", "read")).Get("/agents", server.ListAgents)
			protected.With(apimw.RequirePermission(app, "agents", "read")).Get("/agents/me", server.CurrentAgent)
			protected.With(apimw.RequirePermission(app, "agents", "read")).Get("/agents/{id}", server.GetAgent)
			protected.With(apimw.RequirePermission(app, "agents", "write")).Patch("/agents/{id}", server.UpdateAgent)
			protected.With(apimw.RequirePermission(app, "agents", "manage")).Delete("/agents/{id}", server.DeleteAgent)
			protected.With(apimw.RequirePermission(app, "agents", "manage")).Post("/agents/{id}/rotate-key", server.RotateAgentKey)
			protected.With(apimw.RequirePermission(app, "messages", "read")).Get("/agents/{id}/messages", server.AgentMessages)
			protected.With(apimw.RequirePermission(app, "topics", "read")).Get("/agents/{id}/topics", server.AgentTopics)

			// Topics
			protected.With(apimw.RequirePermission(app, "topics", "write")).Post("/topics", server.CreateTopic)
			protected.With(apimw.RequirePermission(app, "topics", "read")).Get("/topics", server.ListTopics)
			protected.With(apimw.RequirePermission(app, "topics", "read")).Get("/topics/{id}", server.GetTopic)
			protected.With(apimw.RequirePermission(app, "topics", "write")).Patch("/topics/{id}", server.UpdateTopic)
			protected.With(apimw.RequirePermission(app, "topics", "manage")).Delete("/topics/{id}", server.DeleteTopic)
			protected.With(apimw.RequirePermission(app, "topics", "read")).Get("/topics/{id}/subscribers", server.TopicSubscribers)
			protected.With(apimw.RequirePermission(app, "messages", "read")).Get("/topics/{id}/messages", server.TopicMessages)
			protected.With(apimw.RequirePermission(app, "topics", "write")).Post("/topics/{id}/subscribe", server.SubscribeTopic)
			protected.With(apimw.RequirePermission(app, "topics", "write")).Delete("/topics/{id}/subscribe", server.UnsubscribeTopic)
			protected.With(apimw.RequirePermission(app, "topics", "manage")).Post("/topics/{id}/members", server.AddTopicMember)
			protected.With(apimw.RequirePermission(app, "topics", "manage")).Delete("/topics/{id}/members/{agent_id}", server.RemoveTopicMember)
			protected.With(apimw.RequirePermission(app, "topics", "read")).Get("/topics/{id}/members", server.ListTopicMembers)

			// Groups
			protected.With(apimw.RequirePermission(app, "groups", "write")).Post("/groups", server.CreateGroup)
			protected.With(apimw.RequirePermission(app, "groups", "read")).Get("/groups", server.ListGroups)
			protected.With(apimw.RequirePermission(app, "groups", "read")).Get("/groups/{id}", server.GetGroup)
			protected.With(apimw.RequirePermission(app, "groups", "write")).Patch("/groups/{id}", server.UpdateGroup)
			protected.With(apimw.RequirePermission(app, "groups", "manage")).Delete("/groups/{id}", server.DeleteGroup)
			protected.With(apimw.RequirePermission(app, "groups", "write")).Post("/groups/{id}/members", server.AddGroupMember)
			protected.With(apimw.RequirePermission(app, "groups", "manage")).Delete("/groups/{id}/members/{agent_id}", server.RemoveGroupMember)
			protected.With(apimw.RequirePermission(app, "groups", "read")).Get("/groups/{id}/members", server.ListGroupMembers)

			// Messages
			protected.With(apimw.RequirePermission(app, "messages", "write")).Post("/messages", server.CreateMessage)
			protected.With(apimw.RequirePermission(app, "messages", "write")).Post("/messages/broadcast", server.BroadcastMessage)
			protected.With(apimw.RequirePermission(app, "messages", "read")).Get("/messages", server.Inbox)
			protected.With(apimw.RequirePermission(app, "messages", "read")).Get("/messages/{id}", server.GetMessage)
			protected.With(apimw.RequirePermission(app, "messages", "read")).Post("/messages/claim", server.ClaimMessages)
			protected.With(apimw.RequirePermission(app, "messages", "write")).Post("/messages/{id}/ack", server.AckMessageClaim)
			protected.With(apimw.RequirePermission(app, "messages", "write")).Post("/messages/{id}/nack", server.NackMessageClaim)
			protected.With(apimw.RequirePermission(app, "messages", "write")).Post("/messages/{id}/renew", server.RenewMessageClaim)
			protected.With(apimw.RequirePermission(app, "messages", "write")).Post("/messages/{id}/read", server.MarkRead)
			protected.With(apimw.RequirePermission(app, "messages", "read")).Get("/messages/{id}/thread", server.MessageThread)
			protected.With(apimw.RequirePermission(app, "messages", "write")).Delete("/messages/{id}", server.DeleteMessage)

			// Knowledge
			protected.With(apimw.RequirePermission(app, "knowledge", "write")).Post("/knowledge", server.CreateKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "read")).Get("/knowledge", server.ListKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "read")).Get("/knowledge/{id}", server.GetKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "write")).Put("/knowledge/{id}", server.ReplaceKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "write")).Patch("/knowledge/{id}", server.PatchKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "manage")).Delete("/knowledge/{id}", server.DeleteKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "read")).Get("/knowledge/{id}/history", server.KnowledgeHistory)
			protected.With(apimw.RequirePermission(app, "knowledge", "read")).Get("/knowledge/{id}/versions/{v}", server.KnowledgeVersion)
			protected.With(apimw.RequirePermission(app, "knowledge", "write")).Post("/knowledge/{id}/restore/{v}", server.RestoreKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "write")).Post("/knowledge/{id}/pin", server.PinKnowledge)
			protected.With(apimw.RequirePermission(app, "knowledge", "write")).Delete("/knowledge/{id}/pin", server.UnpinKnowledge)

			// Collections
			protected.With(apimw.RequirePermission(app, "collections", "write")).Post("/collections", server.CreateCollection)
			protected.With(apimw.RequirePermission(app, "collections", "read")).Get("/collections", server.ListCollections)
			protected.With(apimw.RequirePermission(app, "collections", "read")).Get("/collections/{id}", server.GetCollection)
			protected.With(apimw.RequirePermission(app, "collections", "write")).Patch("/collections/{id}", server.UpdateCollection)
			protected.With(apimw.RequirePermission(app, "collections", "manage")).Delete("/collections/{id}", server.DeleteCollection)
			protected.With(apimw.RequirePermission(app, "knowledge", "read")).Get("/collections/{id}/knowledge", server.CollectionKnowledge)
			protected.With(apimw.RequirePermission(app, "collections", "read")).Get("/collections/tree", server.CollectionTree)

			// Sync
			protected.With(apimw.RequirePermission(app, "sync", "read")).Get("/sync/remotes", server.ListRemotes)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/remotes", server.AddRemote)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Delete("/sync/remotes/{name}", server.DeleteRemote)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/push", server.Push)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/pull", server.Pull)
			protected.With(apimw.RequirePermission(app, "sync", "read")).Get("/sync/status", server.SyncStatus)
			protected.With(apimw.RequirePermission(app, "sync", "read")).Get("/sync/logs", server.SyncLogs)
			protected.With(apimw.RequirePermission(app, "sync", "read")).Get("/sync/diff", server.SyncDiff)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/diff", server.SyncDiff)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/push/inbound", server.InboundPush)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/pull/inbound", server.InboundPull)
			protected.With(apimw.RequirePermission(app, "sync", "read")).Get("/sync/conflicts", server.ListConflicts)
			protected.With(apimw.RequirePermission(app, "sync", "write")).Post("/sync/conflicts/{id}/resolve", server.ResolveConflict)

			// Admin
			protected.With(apimw.RequirePermission(app, "admin", "read")).Get("/admin/stats", server.AdminStats)
			protected.With(apimw.RequirePermission(app, "admin", "read")).Get("/admin/config", server.AdminConfig)
			protected.With(apimw.RequirePermission(app, "admin", "write")).Post("/admin/backup", server.AdminBackup)
			protected.With(apimw.RequirePermission(app, "admin", "write")).Post("/admin/vacuum", server.AdminVacuum)
			protected.With(apimw.RequirePermission(app, "admin", "write")).Delete("/admin/messages/expired", server.PurgeExpiredMessages)
			protected.With(apimw.RequirePermission(app, "admin", "manage")).Get("/admin/rbac/roles", server.RBACRoles)
			protected.With(apimw.RequirePermission(app, "admin", "manage")).Post("/admin/rbac/assign", server.RBACAssign)
			protected.With(apimw.RequirePermission(app, "admin", "manage")).Delete("/admin/rbac/assign", server.RBACRevoke)
		})
	})

	return r
}
