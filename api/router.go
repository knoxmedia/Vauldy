package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/handler"
	"knox-media/api/middleware"
	"knox-media/cmd/scheduler"
	"knox-media/internal/app"
	"knox-media/internal/atrack"
	"knox-media/internal/config"
	"knox-media/internal/jit/session"
	"knox-media/internal/keyframe"
	"knox-media/internal/preview"
	"knox-media/internal/subtitle"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
)

func NewEngine(cfg *config.Config, application *app.App, worker *transcode.Worker, packageWorker *transcode.PackageWorker, previewWorker *preview.Worker, sub *subtitle.Service, up *upload.Service, instant *scheduler.Scheduler, sm *session.Manager, atw *atrack.Worker, kfw *keyframe.Worker) *gin.Engine {
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(middleware.CORS(cfg.CORS.AllowOrigins))
	r.Static("/uploads", cfg.Data.Upload)
	r.Static("/atracks", cfg.Data.ATracks)
	r.Static("/static", cfg.Data.Static)

	h := handler.New(application, worker, packageWorker, previewWorker, sub, up, instant, sm, atw, kfw)
	go h.StartScheduleLoop(context.Background())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "knox-media"})
	})

	v1 := r.Group("/api/v1")
	{
		v1.POST("/user/login", h.Login)
		v1.POST("/oauth/token", h.OAuthToken)

		// Authenticated (admin or user): account + browsing
		auth := v1.Group("")
		auth.Use(middleware.RequireAuthentication(cfg, false))
		{
			auth.GET("/user/info", h.UserInfo)
			auth.PUT("/user/profile", h.UpdateUserProfile)
			auth.PUT("/user/password", h.ChangeUserPassword)
			auth.POST("/user/avatar", h.UploadUserAvatar)
			auth.DELETE("/user/avatar", h.DeleteUserAvatar)
			auth.GET("/user/history", h.UserHistory)
			auth.POST("/user/logout", h.Logout)
			auth.POST("/user/parental/unlock", h.UnlockParental)

			auth.GET("/library", h.ListLibraries)
			auth.GET("/favorites", h.ListFavorites)
			auth.GET("/media", h.ListMedia)
			auth.GET("/media/:id/favorite", h.FavoriteStatus)
			auth.POST("/media/:id/favorite", h.AddFavorite)
			auth.DELETE("/media/:id/favorite", h.RemoveFavorite)
			auth.GET("/media/:id", h.GetMedia)
			auth.GET("/media/:id/meta", h.GetMediaMeta)
			auth.GET("/media/:id/stats", h.GetMediaStats)
			auth.GET("/media/:id/subtitles", h.ListMediaSubtitles)

			auth.GET("/playlists", h.ListPlaylists)
			auth.POST("/playlists", h.CreatePlaylist)
			auth.GET("/playlists/:id", h.GetPlaylist)
			auth.PUT("/playlists/:id", h.UpdatePlaylist)
			auth.DELETE("/playlists/:id", h.DeletePlaylist)
			auth.POST("/playlists/:id/images/:field", h.UploadPlaylistImage)
			auth.POST("/playlists/:id/items", h.AddPlaylistItem)
			auth.DELETE("/playlists/:id/items/:itemId", h.RemovePlaylistItem)
			auth.PUT("/playlists/:id/reorder", h.ReorderPlaylistItems)

			auth.POST("/media/:id/progress", h.SaveProgress)
			auth.PUT("/media/:id/watched", h.ToggleWatched)
			auth.DELETE("/media/:id/watched", h.ToggleWatched)
		}

		// Playback URLs: allow Bearer or ?access_token= for HTML5 video / players
		play := v1.Group("")
		play.Use(middleware.RequireAuthentication(cfg, true))
		{
			play.GET("/media/:id/play", h.PlayMedia)
			play.POST("/media/:id/playback/start", h.PlaybackStart)
			play.POST("/media/:id/playback/end", h.PlaybackEnd)
			play.GET("/media/:id/hls", h.HLSInfo)
			play.GET("/media/:id/hls/*asset", h.HLSAsset)
			play.GET("/media/:id/dash/*asset", h.DashAsset)
			play.GET("/media/:id/preview", h.PreviewInfo)
			play.GET("/media/:id/preview/sprite.jpg", h.PreviewSprite)
			play.GET("/media/:id/preview/thumbs.vtt", h.PreviewVTT)
			play.GET("/media/:id/subtitles/:sid/vtt", h.SubtitleVTT)
			play.GET("/transcode/task/:id/status", h.GetTranscodeTaskStatus)
			play.GET("/drm/widevine/service-cert", h.WidevineServiceCert)
			play.POST("/drm/widevine/license", h.WidevineLicense)
			play.GET("/drm/powerdrm/key", h.PowerDRMKey)
			play.GET("/drm/hls/aes128/key", h.HLSAES128Key)
			play.GET("/drm/fairplay/cert", h.FairPlayCert)
			play.POST("/drm/fairplay/license", h.FairPlayLicense)
			if instant != nil {
				instant.RegisterRoutes(play)
			}
			// New Redis-free JIT session routes.
			if sm != nil {
				play.GET("/jit/session/:sessionID/*asset", h.ServeJITAsset)
				play.POST("/jit/session/:sessionID/pause", h.PauseJITSession)
				play.POST("/jit/session/:sessionID/resume", h.ResumeJITSession)
				play.POST("/jit/session/:sessionID/seek", h.SeekJITSession)
				play.POST("/jit/session/:sessionID/end", h.EndJITSession)
			}
		}

		admStream := v1.Group("")
		admStream.Use(middleware.RequireAuthentication(cfg, true))
		admStream.Use(middleware.RequireAdmin())
		{
			admStream.GET("/admin/overview/stream", h.AdminOverviewStream)
		}

		// Admin only: media management + uploads + transcode control
		adm := v1.Group("")
		adm.Use(middleware.RequireAuthentication(cfg, false))
		adm.Use(middleware.RequireAdmin())
		{
			adm.POST("/library", h.CreateLibrary)
			adm.PUT("/library/:id", h.UpdateLibrary)
			adm.DELETE("/library/:id", h.DeleteLibrary)
			adm.POST("/library/:id/scan", h.ScanLibrary)
			adm.GET("/scan/task", h.ListScanTasks)
			adm.POST("/scan/task/:id/cancel", h.CancelScanTask)

			adm.POST("/media/:id/scrape", h.ScrapeMedia)
			adm.POST("/media/:id/subtitle/process", h.ProcessMediaSubtitles)
			adm.POST("/media/:id/manual-match", h.ManualMatchMedia)
			adm.PATCH("/media/:id/meta", h.UpdateMediaMetadata)
			adm.PATCH("/media/:id/images", h.UpdateMediaImages)
			adm.PUT("/media/:id", h.UpdateMediaAdmin)

			adm.GET("/scrape/config", h.GetScrapeConfig)
			adm.PUT("/scrape/config", h.SaveScrapeConfig)
			adm.GET("/ai-provider", h.ListAIProviders)
			adm.PUT("/ai-provider/:id", h.SaveAIProvider)
			adm.GET("/scrape/task", h.ListScrapeTasks)
			adm.POST("/scrape/task", h.CreateScrapeTasks)
			adm.POST("/scrape/task/run", h.RunScrapeTasks)
			adm.GET("/scrape/history", h.ListScrapeHistory)
			adm.GET("/scrape/tmdb/images", h.SearchTMDbImages)

			adm.POST("/upload", h.UploadSingle)
			adm.POST("/upload/mkdir", h.CreateUploadDirectory)
			adm.POST("/upload/image", h.UploadImage)
			adm.POST("/upload/chunk", h.UploadChunk)
			adm.POST("/upload/merge", h.UploadMerge)

			adm.POST("/transcode/async", h.TranscodeAsync)
			adm.GET("/transcode/task", h.ListTranscodeTasks)
			adm.POST("/transcode/task/:id/cancel", h.CancelTranscodeTask)
			adm.POST("/transcode/task/:id/retry", h.RetryTranscodeTask)
			adm.POST("/transcode/task/cleanup-failed", h.CleanupFailedTranscodeTasks)
			adm.POST("/transcode/task/cleanup-failed-before", h.CleanupFailedTranscodeTasksBefore)
			adm.POST("/transcode/drm/repair", h.RepairDRMOutputs)
			adm.GET("/preview/task", h.ListPreviewTasks)
			adm.POST("/preview/task/:mediaId/retry", h.RetryPreviewTask)
			adm.GET("/subtitle/task", h.ListSubtitleTasks)
			adm.POST("/subtitle/task/:mediaId/reset", h.ResetSubtitleTask)
			adm.POST("/subtitle/task/:mediaId/retry", h.RetrySubtitleTask)
			adm.POST("/subtitle/task/cleanup-failed", h.CleanupSubtitleTasksFailed)
			adm.POST("/subtitle/task/cleanup-before", h.CleanupSubtitleTasksBefore)
			adm.POST("/media/:id/atrack", h.EnqueueAudioTrackExtraction)
			adm.GET("/atrack/task", h.ListAudioTrackTasks)
			adm.POST("/atrack/task/:mediaId/retry", h.RetryAudioTrackTask)
			adm.POST("/media/:id/keyframe", h.EnqueueKeyframeExtraction)
			adm.GET("/keyframe/task", h.ListKeyframeTasks)
			adm.POST("/keyframe/task/:mediaId/retry", h.RetryKeyframeTask)
			adm.GET("/schedule/task", h.ListScheduledTasks)
			adm.POST("/schedule/task", h.CreateScheduledTask)
			adm.PUT("/schedule/task/:id", h.UpdateScheduledTask)
			adm.DELETE("/schedule/task/:id", h.DeleteScheduledTask)
			adm.POST("/schedule/task/:id/run", h.RunScheduledTask)
			adm.GET("/admin/overview", h.AdminOverview)
			adm.GET("/admin/access-log", h.ListAccessLogs)
			adm.GET("/admin/drm-license-audit", h.ListDRMLicenseAudits)
			adm.POST("/admin/drm/license/verify", h.VerifyLicense)

			adm.GET("/admin/api-clients", h.ListAPIClients)
			adm.POST("/admin/api-clients", h.CreateAPIClient)
			adm.DELETE("/admin/api-clients/:id", h.RevokeAPIClient)
			adm.GET("/admin/users", h.ListUsersAdmin)
			adm.POST("/admin/users", h.CreateUserAdmin)
			adm.PUT("/admin/users/:id", h.UpdateUserAdmin)
			adm.DELETE("/admin/users/:id", h.DeleteUserAdmin)
			adm.POST("/admin/users/:id/reset-password", h.ResetUserPasswordAdmin)
		}
	}

	webDist := "web/dist"
	if fi, err := os.Stat(webDist); err == nil && fi.IsDir() {
		r.Static("/assets", webDist+"/assets")
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			c.File(webDist + "/index.html")
		})
	}

	return r
}
