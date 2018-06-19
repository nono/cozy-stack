//go:generate statik -f -src=../assets -dest=. -externals=../assets/.externals

package web

import (
	"strconv"
	"time"

	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/metrics"
	"github.com/cozy/cozy-stack/web/apps"
	"github.com/cozy/cozy-stack/web/auth"
	"github.com/cozy/cozy-stack/web/data"
	"github.com/cozy/cozy-stack/web/errors"
	"github.com/cozy/cozy-stack/web/files"
	"github.com/cozy/cozy-stack/web/index"
	"github.com/cozy/cozy-stack/web/instances"
	"github.com/cozy/cozy-stack/web/intents"
	"github.com/cozy/cozy-stack/web/jobs"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/cozy-stack/web/konnectorsauth"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/move"
	"github.com/cozy/cozy-stack/web/notifications"
	"github.com/cozy/cozy-stack/web/permissions"
	"github.com/cozy/cozy-stack/web/realtime"
	"github.com/cozy/cozy-stack/web/registry"
	"github.com/cozy/cozy-stack/web/remote"
	"github.com/cozy/cozy-stack/web/settings"
	"github.com/cozy/cozy-stack/web/sharings"
	"github.com/cozy/cozy-stack/web/statik"
	"github.com/cozy/cozy-stack/web/status"
	"github.com/cozy/cozy-stack/web/version"

	"github.com/cozy/echo"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// cspScriptSrcWhitelist is a whitelist for default allowed domains in CSP.
	cspScriptSrcWhitelist = "https://piwik.cozycloud.cc"

	// cspImgSrcWhitelist is a whitelist of images domains that are allowed in
	// CSP.
	cspImgSrcWhitelist = "https://piwik.cozycloud.cc " +
		"https://*.tile.openstreetmap.org https://*.tile.osm.org " +
		"https://*.tiles.mapbox.com https://api.mapbox.com"
)

var hstsMaxAge = 365 * 24 * time.Hour // 1 year

// SetupAppsHandler adds all the necessary middlewares for the application
// handler.
func SetupAppsHandler(appsHandler echo.HandlerFunc) echo.HandlerFunc {
	mws := []echo.MiddlewareFunc{
		middlewares.LoadAppSession,
	}
	if !config.GetConfig().CSPDisabled {
		secure := middlewares.Secure(&middlewares.SecureConfig{
			HSTSMaxAge:    hstsMaxAge,
			CSPDefaultSrc: []middlewares.CSPSource{middlewares.CSPSrcSelf, middlewares.CSPSrcParent, middlewares.CSPSrcWS},
			CSPStyleSrc:   []middlewares.CSPSource{middlewares.CSPUnsafeInline},
			CSPFontSrc:    []middlewares.CSPSource{middlewares.CSPSrcData},
			CSPImgSrc:     []middlewares.CSPSource{middlewares.CSPSrcData, middlewares.CSPSrcBlob},
			CSPFrameSrc:   []middlewares.CSPSource{middlewares.CSPSrcSiblings},

			CSPDefaultSrcWhitelist: config.GetConfig().CSPWhitelist["default"],
			CSPImgSrcWhitelist:     config.GetConfig().CSPWhitelist["img"] + " " + cspImgSrcWhitelist,
			CSPScriptSrcWhitelist:  config.GetConfig().CSPWhitelist["script"] + " " + cspScriptSrcWhitelist,
			CSPConnectSrcWhitelist: config.GetConfig().CSPWhitelist["connect"] + " " + cspScriptSrcWhitelist,
			CSPStyleSrcWhitelist:   config.GetConfig().CSPWhitelist["style"],
			CSPFontSrcWhitelist:    config.GetConfig().CSPWhitelist["font"],

			XFrameOptions: middlewares.XFrameSameOrigin,
		})
		mws = append([]echo.MiddlewareFunc{secure}, mws...)
	}

	return middlewares.Compose(appsHandler, mws...)
}

// SetupAssets add assets routing and handling to the given router. It also
// adds a Renderer to render templates.
func SetupAssets(router *echo.Echo, assetsPath string) (err error) {
	var r statik.AssetRenderer
	if assetsPath != "" {
		r, err = statik.NewDirRenderer(assetsPath)
	} else {
		r, err = statik.NewRenderer()
	}
	if err != nil {
		return err
	}

	cacheControl := middlewares.CacheControl(middlewares.CacheOptions{
		MaxAge: 24 * time.Hour,
	})

	router.Renderer = r
	router.HEAD("/assets/*", echo.WrapHandler(r))
	router.GET("/assets/*", echo.WrapHandler(r))
	router.GET("/favicon.ico", echo.WrapHandler(r), cacheControl)
	router.GET("/robots.txt", echo.WrapHandler(r), cacheControl)
	router.GET("/security.txt", echo.WrapHandler(r), cacheControl)
	return nil
}

// SetupRoutes sets the routing for HTTP endpoints
func SetupRoutes(router *echo.Echo) error {
	router.Use(timersMiddleware)

	if !config.GetConfig().CSPDisabled {
		secure := middlewares.Secure(&middlewares.SecureConfig{
			HSTSMaxAge:    hstsMaxAge,
			CSPDefaultSrc: []middlewares.CSPSource{middlewares.CSPSrcSelf},
			XFrameOptions: middlewares.XFrameDeny,
		})
		router.Use(secure)
	}

	router.Use(middlewares.CORS(middlewares.CORSOptions{
		BlackList: []string{"/auth/"},
	}))

	// non-authentified HTML routes for authentication (login, OAuth, ...)
	{
		mws := []echo.MiddlewareFunc{
			middlewares.NeedInstance,
			middlewares.LoadSession,
			middlewares.Accept(middlewares.AcceptOptions{
				DefaultContentTypeOffer: echo.MIMETextHTML,
			}),
		}
		router.GET("/", auth.Home, mws...)
		auth.Routes(router.Group("/auth", mws...))
	}

	// authentified JSON API routes
	{
		mwsNotBlocked := []echo.MiddlewareFunc{
			middlewares.NeedInstance,
			middlewares.LoadSession,
			middlewares.Accept(middlewares.AcceptOptions{
				DefaultContentTypeOffer: jsonapi.ContentType,
			}),
		}
		mws := append(mwsNotBlocked, middlewares.CheckInstanceTOS)
		apps.WebappsRoutes(router.Group("/apps", mws...))
		apps.KonnectorRoutes(router.Group("/konnectors", mws...))
		registry.Routes(router.Group("/registry", mws...))
		data.Routes(router.Group("/data", mws...))
		files.Routes(router.Group("/files", mws...))
		intents.Routes(router.Group("/intents", mws...))
		jobs.Routes(router.Group("/jobs", mws...))
		notifications.Routes(router.Group("/notifications", mws...))
		move.Routes(router.Group("/move", mws...))
		permissions.Routes(router.Group("/permissions", mws...))
		realtime.Routes(router.Group("/realtime", mws...))
		remote.Routes(router.Group("/remote", mws...))
		sharings.Routes(router.Group("/sharings", mws...))
		index.Routes(router.Group("/index", mws...))

		// The settings routes needs not to be blocked
		settings.Routes(router.Group("/settings", mwsNotBlocked...))

		// Careful, the normal middlewares NeedInstance and LoadSession are not
		// applied to this group in web/routing since they should not be used for
		// oauth redirection.
		konnectorsauth.Routes(router.Group("/accounts"))
	}

	// non-authentified JSON API routes
	{
		status.Routes(router.Group("/status"))
		version.Routes(router.Group("/version"))
	}

	// dev routes
	if config.IsDevRelease() {
		router.GET("/dev/mails/:name", devMailsHandler)
		router.GET("/dev/templates/:name", devTemplatesHandler)
	}

	setupRecover(router)
	router.HTTPErrorHandler = errors.ErrorHandler
	return nil
}

func timersMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		timer := prometheus.NewTimer(prometheus.ObserverFunc(func(v float64) {
			status := strconv.Itoa(c.Response().Status)
			metrics.HTTPTotalDurations.
				WithLabelValues(c.Request().Method, status).
				Observe(v)
		}))
		defer timer.ObserveDuration()
		return next(c)
	}
}

// SetupAdminRoutes sets the routing for the administration HTTP endpoints
func SetupAdminRoutes(router *echo.Echo) error {
	var mws []echo.MiddlewareFunc
	if !config.IsDevRelease() {
		mws = append(mws, middlewares.BasicAuth(config.GetConfig().AdminSecretFileName))
	}

	instances.Routes(router.Group("/instances", mws...))
	version.Routes(router.Group("/version", mws...))
	metrics.Routes(router.Group("/metrics", mws...))

	setupRecover(router)

	router.HTTPErrorHandler = errors.ErrorHandler
	return nil
}

// CreateSubdomainProxy returns a new web server that will handle that apps
// proxy routing if the host of the request match an application, and route to
// the given router otherwise.
func CreateSubdomainProxy(router *echo.Echo, appsHandler echo.HandlerFunc) (*echo.Echo, error) {
	if err := SetupAssets(router, config.GetConfig().Assets); err != nil {
		return nil, err
	}

	if err := SetupRoutes(router); err != nil {
		return nil, err
	}

	appsHandler = SetupAppsHandler(appsHandler)

	main := echo.New()
	main.HideBanner = true
	main.HidePort = true
	main.Renderer = router.Renderer
	main.Any("/*", func(c echo.Context) error {
		// TODO(optim): minimize the number of instance requests
		if parent, slug, _ := middlewares.SplitHost(c.Request().Host); slug != "" {
			if i, err := instance.Get(parent); err == nil {
				c.Set("instance", i)
				c.Set("slug", slug)
				return appsHandler(c)
			}
		}

		router.ServeHTTP(c.Response(), c.Request())
		return nil
	})

	main.HTTPErrorHandler = errors.HTMLErrorHandler
	return main, nil
}

// setupRecover sets a recovering strategy of panics happening in handlers
func setupRecover(router *echo.Echo) {
	if !config.IsDevRelease() {
		recoverMiddleware := middlewares.RecoverWithConfig(middlewares.RecoverConfig{
			StackSize: 10 << 10, // 10KB
		})
		router.Use(recoverMiddleware)
	}
}
