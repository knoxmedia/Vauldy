package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/handler"
	"knox-media/api/middleware"
	"knox-media/cmd/scheduler"
	"knox-media/internal/app"
	"knox-media/internal/atrack"
	"knox-media/internal/metadatalib"
	"knox-media/internal/config"
	"knox-media/internal/doccover"
	"knox-media/internal/jit/session"
	"knox-media/internal/keyframe"
	"knox-media/internal/lyrictask"
	"knox-media/internal/photoclass"
	"knox-media/internal/preview"
	"knox-media/internal/subtitle"
	"knox-media/internal/storage"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
)

func NewEngine(cfg *config.Config, application *app.App, worker *transcode.Worker, packageWorker *transcode.PackageWorker, previewWorker *preview.Worker, sub *subtitle.Service, up *upload.Service, instant *scheduler.Scheduler, sm *session.Manager, atw *atrack.Worker, kfw *keyframe.Worker, lw *lyrictask.Worker, pcw *photoclass.Worker, dcw *doccover.Worker) *gin.Engine {
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(middleware.CORS(cfg.CORS.AllowOrigins))
	r.Static("/uploads", cfg.Data.Upload)
	r.Static("/atracks", cfg.Data.ATracks)
	webBundle := resolveWebBundle()
	mountStaticRoutes(r, cfg.Data.Static, resolvePowerPlayerStatic(webBundle))
	r.Static(metadatalib.PublicURLPrefix, cfg.Data.MetadataLibrary)

	keyVault, assetEnc := storage.NewAssetEncryptorFromConfig(cfg, application.DB)
	derivedStore := storage.NewDerivedAssetStoreFromConfig(cfg, application.DB, keyVault)
	h := handler.New(application, worker, packageWorker, previewWorker, sub, up, instant, sm, atw, kfw, lw, pcw, dcw, keyVault, assetEnc, derivedStore)
	if dcw != nil {
		dcw.SetOnCoverReady(h.ScheduleLibraryPreviewRefreshForMedia)
	}
	go h.StartScheduleLoop(context.Background())
	go h.StartScrapeTaskLoop(context.Background())
	go h.StartSubtitleTaskLoop(context.Background())
	go h.StartKeyframeTaskLoop(context.Background())
	go h.StartAtrackTaskLoop(context.Background())
	go h.StartPreviewTaskLoop(context.Background())
	go h.StartTranscodeTaskLoop(context.Background())
	go h.StartLyricTaskLoop(context.Background())
	go h.StartPhotoClassifyLoop(context.Background())
	go h.StartPhotoLocationLoop(context.Background())
	go h.StartPhotoFaceLoop(context.Background())
	if dcw != nil {
		go dcw.Start(context.Background())
		go func() {
			// Allow worker loop to start before backfill storm.
			time.Sleep(500 * time.Millisecond)
			dcw.BackfillAllLibraries()
		}()
	}

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "knox-media"})
	})
	r.GET("/favicon.svg", h.ServeBrandingFavicon)
	r.GET("/favicon.ico", h.ServeBrandingFavicon)

	v1 := r.Group("/api/v1")
	{
		v1.POST("/user/login", h.Login)
		v1.POST("/oauth/token", h.OAuthToken)
		v1.GET("/branding", h.GetBranding)

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
			auth.GET("/playback-history", h.ListPlaybackHistory)
			auth.POST("/user/logout", h.Logout)
			auth.POST("/user/parental/unlock", h.UnlockParental)

			auth.GET("/library", h.ListLibraries)
			auth.GET("/library/:id/series", h.ListLibrarySeries)
			auth.GET("/library/:id/albums", h.ListLibraryAlbums)
			auth.GET("/library/:id/artists", h.ListLibraryArtists)
			auth.GET("/library/:id/genres", h.ListLibraryGenres)
			auth.GET("/library/:id/genre/albums", h.ListGenreAlbums)
			auth.GET("/library/:id/tracks", h.ListLibraryTracks)
			auth.GET("/library/:id/photo/categories", h.ListPhotoCategories)
			auth.GET("/library/:id/photo/places", h.ListPhotoPlaces)
			auth.POST("/library/:id/photo/locations/backfill", h.BackfillPhotoLocations)
			auth.GET("/library/:id/photo/locations/progress", h.PhotoLocationProgress)
			auth.GET("/library/:id/photo/persons", h.ListPhotoPersons)
			auth.PATCH("/library/:id/photo/persons/:personId", h.UpdatePhotoPerson)
			auth.POST("/library/:id/photo/faces/backfill", h.BackfillPhotoFaces)
			auth.GET("/library/:id/photo/faces/progress", h.PhotoFaceProgress)
			auth.GET("/library/:id/photo/classify/progress", h.PhotoClassifyProgress)
			auth.PATCH("/media/:id/photo/tags", h.UpdatePhotoTags)
			auth.GET("/library/:id/documents", h.ListDocuments)
			auth.GET("/library/:id/document/nodes", h.ListDocumentNodes)
			auth.GET("/library/:id/document/facets", h.ListDocumentFacets)
			auth.GET("/library/:id/documents/recent", h.ListRecentDocuments)
			auth.GET("/media/:id/document", h.GetDocumentDetail)
			auth.GET("/media/:id/document/preview/info", h.DocumentPreviewInfo)
			auth.PATCH("/media/:id/document", h.UpdateDocumentMeta)
			auth.GET("/media/:id/read-progress", h.GetReadProgress)
			auth.POST("/media/:id/read-progress", h.SaveReadProgress)
			auth.POST("/documents/download", h.BatchDownloadDocuments)
			auth.GET("/scan-logs", h.ListScanLogs)
			auth.GET("/album/:id", h.GetAlbum)
			auth.GET("/album/:id/play-target", h.GetAlbumPlayTarget)
			auth.GET("/album/:id/image-candidates", h.ListAlbumImageCandidates)
			auth.PATCH("/album/:id", h.UpdateAlbum)
			auth.GET("/artist/:id/albums", h.ListArtistAlbums)
			auth.GET("/artist/:id", h.GetArtist)
			auth.PATCH("/artist/:id", h.UpdateArtist)
			auth.GET("/artist/:id/image-candidates", h.ListArtistImageCandidates)
			auth.PATCH("/library/:id/genre", h.UpdateLibraryGenre)
			auth.GET("/series/:id", h.GetSeries)
			auth.GET("/series/:id/play-target", h.GetSeriesPlayTarget)
			auth.GET("/series/:id/image-candidates", h.ListSeriesImageCandidates)
			auth.PATCH("/series/:id", h.UpdateSeries)
			auth.GET("/season/:id/episodes", h.ListSeasonEpisodes)
			auth.GET("/favorites", h.ListFavorites)
			auth.GET("/favorite-folders", h.ListFavoriteFolders)
			auth.POST("/favorite-folders", h.CreateFavoriteFolder)
			auth.GET("/favorite-folders/:id", h.GetFavoriteFolder)
			auth.PUT("/favorite-folders/:id", h.UpdateFavoriteFolder)
			auth.DELETE("/favorite-folders/:id", h.DeleteFavoriteFolder)
			auth.POST("/favorite-folders/:id/items", h.AddFavoriteFolderItem)
			auth.DELETE("/favorite-folders/:id/items/:itemId", h.RemoveFavoriteFolderItem)
			auth.GET("/media", h.ListMedia)
			auth.GET("/media/:id/favorite", h.FavoriteStatus)
			auth.POST("/media/:id/favorite", h.AddFavorite)
			auth.DELETE("/media/:id/favorite", h.RemoveFavorite)
			auth.GET("/media/:id", h.GetMedia)
			auth.GET("/media/:id/meta", h.GetMediaMeta)
			auth.GET("/media/:id/stats", h.GetMediaStats)
			auth.GET("/media/:id/subtitles", h.ListMediaSubtitles)
			auth.GET("/media/:id/lyrics", h.GetMediaLyrics)

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
			auth.DELETE("/media/:id/progress", h.ClearProgress)
			auth.PUT("/media/:id/watched", h.ToggleWatched)
			auth.DELETE("/media/:id/watched", h.ToggleWatched)
		}

		// Playback URLs: allow Bearer or ?access_token= for HTML5 video / players
		play := v1.Group("")
		play.Use(middleware.RequireAuthentication(cfg, true))
		{
			play.GET("/proxy/image", h.ProxyRemoteImage)
			play.GET("/media/:id/play", h.PlayMedia)
			play.POST("/media/:id/playback/start", h.PlaybackStart)
			play.POST("/media/:id/playback/end", h.PlaybackEnd)
			play.GET("/media/:id/hls", h.HLSInfo)
			play.GET("/media/:id/hls/*asset", h.HLSAsset)
			play.GET("/media/:id/dash/*asset", h.DashAsset)
			play.GET("/media/:id/preview", h.PreviewInfo)
			play.GET("/media/:id/preview/sprite.jpg", h.PreviewSprite)
			play.GET("/media/:id/preview/thumbs.vtt", h.PreviewVTT)
			play.GET("/media/:id/poster.jpg", h.ServeMediaPoster)
			play.GET("/album/:id/artwork", h.ServeAlbumArtwork)
			play.GET("/artist/:id/artwork", h.ServeArtistArtwork)
			play.GET("/media/:id/photo", h.PhotoPreviewInfo)
			play.GET("/media/:id/photo/thumb.jpg", h.ServePhotoThumb)
			play.GET("/media/:id/photo/medium.jpg", h.ServePhotoMedium)
			play.GET("/media/:id/document/cover.jpg", h.ServeDocumentCover)
			play.GET("/media/:id/document/preview.pdf", h.ServeDocumentPreview)
			play.GET("/photo/face/:id/thumb.jpg", h.ServePhotoFaceThumb)
			play.GET("/media/:id/subtitles/:sid/vtt", h.SubtitleVTT)
			play.POST("/subtitles/translate", h.TranslateSubtitle)
			play.GET("/media/:id/atrack/:stream/index.m3u8", h.ServeAtrackPlaylist)
			play.GET("/media/:id/atrack/:stream/seg/:seg", h.ServeAtrackSegment)
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
			adm.POST("/library/:id/photo/classify", h.EnqueuePhotoLibraryClassify)
			adm.GET("/scan/task", h.ListScanTasks)
			adm.POST("/scan/task/:id/cancel", h.CancelScanTask)

			adm.POST("/media/:id/scrape", h.ScrapeMedia)
			adm.POST("/media/:id/subtitle/process", h.ProcessMediaSubtitles)
			adm.POST("/media/:id/subtitle", h.EnqueueSubtitleProcessing)
			adm.GET("/media/:id/subtitles/:sid/cues", h.GetSubtitleCues)
			adm.PUT("/media/:id/subtitles/:sid/cues", h.SaveSubtitleCues)
			adm.POST("/media/:id/subtitles/import", h.ImportSubtitle)
			adm.PUT("/media/:id/lyrics", h.SaveMediaLyrics)
			adm.POST("/media/:id/lyrics/import", h.ImportMediaLyrics)
			adm.POST("/media/:id/lyrics/recognize", h.EnqueueLyricRecognition)
			adm.POST("/media/:id/manual-match", h.ManualMatchMedia)
			adm.DELETE("/media/:id/match", h.UnmatchMedia)
			adm.PATCH("/media/:id/meta", h.UpdateMediaMetadata)
			adm.PATCH("/media/:id/images", h.UpdateMediaImages)
			adm.GET("/media/:id/image-candidates", h.ListMediaImageCandidates)
			adm.PUT("/media/:id", h.UpdateMediaAdmin)
			adm.GET("/media/:id/deletion-plan", h.GetMediaDeletionPlan)
			adm.DELETE("/media/:id", h.DeleteMedia)

			adm.GET("/scrape/config", h.GetScrapeConfig)
			adm.PUT("/scrape/config", h.SaveScrapeConfig)
			adm.POST("/scrape/config/test", h.TestScrapeProvider)
			adm.GET("/ai-provider", h.ListAIProviders)
			adm.PUT("/ai-provider/:id", h.SaveAIProvider)
			adm.POST("/ai-provider/:id/test", h.TestAIProvider)
			adm.GET("/scrape/task", h.ListScrapeTasks)
			adm.POST("/scrape/task", h.CreateScrapeTasks)
			adm.POST("/scrape/task/run", h.RunScrapeTasks)
			adm.POST("/scrape/artwork/backfill", h.BackfillScrapeArtwork)
			adm.GET("/scrape/history", h.ListScrapeHistory)
			adm.GET("/scrape/search", h.SearchScrapeMatches)
			adm.GET("/scrape/parse-title", h.ParseScrapeTitle)
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
			adm.DELETE("/subtitle/task/:mediaId", h.DeleteSubtitleTask)
			adm.POST("/subtitle/task/cleanup-failed", h.CleanupSubtitleTasksFailed)
			adm.POST("/subtitle/task/cleanup-before", h.CleanupSubtitleTasksBefore)
			adm.GET("/lyric/task", h.ListLyricTasks)
			adm.POST("/lyric/task/:mediaId/retry", h.RetryLyricTask)
			adm.POST("/lyric/task/cleanup-failed", h.CleanupLyricTasksFailed)
			adm.POST("/lyric/task/cleanup-before", h.CleanupLyricTasksBefore)
			adm.POST("/media/:id/atrack", h.EnqueueAudioTrackExtraction)
			adm.GET("/atrack/task", h.ListAudioTrackTasks)
			adm.POST("/atrack/task/:mediaId/retry", h.RetryAudioTrackTask)
			adm.POST("/media/:id/keyframe", h.EnqueueKeyframeExtraction)
			adm.POST("/media/:id/encrypt-assets", h.EncryptMediaAssets)
			adm.GET("/keyframe/task", h.ListKeyframeTasks)
			adm.POST("/keyframe/task/:mediaId/retry", h.RetryKeyframeTask)
			adm.GET("/schedule/task", h.ListScheduledTasks)
			adm.POST("/schedule/task", h.CreateScheduledTask)
			adm.PUT("/schedule/task/:id", h.UpdateScheduledTask)
			adm.DELETE("/schedule/task/:id", h.DeleteScheduledTask)
			adm.POST("/schedule/task/cleanup-duplicates", h.CleanupDuplicateScheduledTasks)
			adm.POST("/schedule/task/:id/run", h.RunScheduledTask)
			adm.PUT("/admin/branding", h.PutBranding)
			adm.GET("/admin/system-options", h.GetSystemOptions)
			adm.PUT("/admin/system-options", h.PutSystemOptions)
			adm.POST("/admin/system-options/test/asr", h.TestSystemOptionsASR)
			adm.POST("/admin/system-options/test/ocr", h.TestSystemOptionsOCR)
			adm.POST("/admin/system-options/install/asr", h.InstallSystemOptionsASR)
			adm.POST("/admin/system-options/install/ocr", h.InstallSystemOptionsOCR)
			adm.POST("/admin/system-options/test/photo-classify", h.TestSystemOptionsPhotoClassify)
			adm.POST("/admin/system-options/install/photo-classify", h.InstallSystemOptionsPhotoClassify)
			adm.POST("/admin/system-options/test/photo-face", h.TestSystemOptionsPhotoFace)
			adm.POST("/admin/system-options/install/photo-face", h.InstallSystemOptionsPhotoFace)
			adm.POST("/admin/system-options/test/doc-trans", h.TestSystemOptionsDocTrans)
			adm.POST("/admin/system-options/install/doc-trans", h.InstallSystemOptionsDocTrans)
			adm.POST("/admin/system-options/install/libreoffice", h.InstallLibreOfficeDocTrans)

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

	mountWebFrontend(r, webBundle)

	return r
}
