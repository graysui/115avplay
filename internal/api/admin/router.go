package admin

import (
	"mediavault/internal/client115"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/services"

	"github.com/gin-gonic/gin"
)

// RegisterAdminRoutes mounts all administrative API endpoints onto the provided gin router group.
func RegisterAdminRoutes(
	r *gin.RouterGroup,
	userRepo *db.UserRepo,
	settingsRepo *db.SettingsRepo,
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	assetRepo *db.AssetRepo,
	libraryRepo *db.LibraryRepo,
	jobRepo *db.JobRepo,
	database *db.DB,
	appConfig *config.AppConfig,
	transferManager *services.TransferManager,
	auth115 *client115.AuthClient,
) {
	authHandler := NewAuthHandler(userRepo)
	statsHandler := NewStatsHandler(database, settingsRepo, appConfig)
	settingsHandler := NewSettingsHandler(settingsRepo, appConfig)
	moviesHandler := NewMoviesHandler(movieRepo, magnetRepo, assetRepo, transferManager)
	librariesHandler := NewLibrariesHandler(libraryRepo)
	usersHandler := NewUsersHandler(userRepo)
	tasksHandler := NewTasksHandler(jobRepo)
	scraperHandler := NewScraperHandler(database, jobRepo)
	schedulesHandler := NewSchedulesHandler(settingsRepo)
	alertsHandler := NewAlertsHandler(settingsRepo)
	cloud115Handler := NewCloud115Handler(assetRepo, settingsRepo, appConfig, auth115)

	// Public Auth
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/login", authHandler.Login)
	}

	// Protected Admin endpoints
	protected := r.Group("")
	protected.Use(AdminAuthMiddleware(userRepo))
	{
		// Session / Profile
		protected.POST("/auth/logout", authHandler.Logout)
		protected.GET("/auth/me", authHandler.Me)
		protected.POST("/auth/password", authHandler.ChangePassword)

		// Dashboard & System Status
		protected.GET("/stats", statsHandler.GetStats)
		protected.GET("/status", statsHandler.GetSystemStatus)

		// Settings
		protected.GET("/settings", settingsHandler.GetSettings)
		protected.PUT("/settings", settingsHandler.UpdateSettings)

		// Movies & Magnets
		protected.GET("/movies", moviesHandler.ListMovies)
		protected.GET("/movies/:code", moviesHandler.GetMovie)
		protected.PUT("/movies/:code", moviesHandler.UpdateMovie)
		protected.DELETE("/movies/:code", moviesHandler.DeleteMovie)
		protected.POST("/movies/:code/magnets", moviesHandler.AddMagnet)
		protected.PUT("/movies/:code/magnets/:hash", moviesHandler.ToggleMagnet)
		protected.POST("/movies/:code/prepare", moviesHandler.PrepareMovie)

		// Libraries
		protected.GET("/libraries", librariesHandler.ListLibraries)
		protected.PUT("/libraries/:id", librariesHandler.UpdateLibrary)

		// User Management
		protected.GET("/users", usersHandler.ListUsers)
		protected.POST("/users", usersHandler.CreateUser)
		protected.PUT("/users/:id", usersHandler.UpdateUser)
		protected.DELETE("/users/:id", usersHandler.DeleteUser)
		protected.POST("/users/:id/reset_password", usersHandler.ResetPassword)

		// Tasks & Jobs
		protected.GET("/tasks", tasksHandler.ListTasks)
		protected.GET("/tasks/:id", tasksHandler.GetTask)
		protected.POST("/tasks/:id/retry", tasksHandler.RetryTask)
		protected.POST("/tasks/:id/cancel", tasksHandler.CancelTask)
		protected.POST("/tasks/trigger", tasksHandler.TriggerTask)

		// Scraper Management
		protected.POST("/scraper/trigger", scraperHandler.TriggerScrape)
		protected.GET("/scraper/status", scraperHandler.GetScraperStatus)

		// Schedules Management
		protected.GET("/schedules", schedulesHandler.ListSchedules)
		protected.POST("/schedules", schedulesHandler.CreateSchedule)
		protected.PUT("/schedules/:id", schedulesHandler.UpdateSchedule)
		protected.DELETE("/schedules/:id", schedulesHandler.DeleteSchedule)

		// Alerts
		protected.GET("/alerts", alertsHandler.ListAlerts)
		protected.POST("/alerts/:key/resolve", alertsHandler.ResolveAlert)

		// 115 Cloud Binding
		protected.GET("/115/status", cloud115Handler.GetStatus)
		protected.POST("/115/auth/device", cloud115Handler.StartDeviceAuth)
		protected.POST("/115/auth/poll", cloud115Handler.PollDeviceToken)

		// WebSocket Log Stream
		protected.GET("/ws/logs", LogsWebSocketHandler)
	}
}
