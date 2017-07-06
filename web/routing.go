//go:generate statik -src=../.assets -dest=.

package web

import (
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"path"
	"time"

	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/web/apps"
	"github.com/cozy/cozy-stack/web/auth"
	"github.com/cozy/cozy-stack/web/data"
	"github.com/cozy/cozy-stack/web/errors"
	"github.com/cozy/cozy-stack/web/files"
	"github.com/cozy/cozy-stack/web/imexport"
	"github.com/cozy/cozy-stack/web/instances"
	"github.com/cozy/cozy-stack/web/intents"
	"github.com/cozy/cozy-stack/web/jobs"
	"github.com/cozy/cozy-stack/web/konnectorsauth"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/notifications"
	"github.com/cozy/cozy-stack/web/permissions"
	"github.com/cozy/cozy-stack/web/realtime"
	"github.com/cozy/cozy-stack/web/registry"
	"github.com/cozy/cozy-stack/web/remote"
	"github.com/cozy/cozy-stack/web/search"
	"github.com/cozy/cozy-stack/web/settings"
	"github.com/cozy/cozy-stack/web/sharings"
	_ "github.com/cozy/cozy-stack/web/statik" // Generated file with the packed assets
	"github.com/cozy/cozy-stack/web/status"
	"github.com/cozy/cozy-stack/web/version"
	"github.com/labstack/echo"
	"github.com/rakyll/statik/fs"
)

var (
	hstsMaxAge = 365 * 24 * time.Hour // 1 year

	templatesList = []string{
		"authorize.html",
		"authorize_app.html",
		"authorize_sharing.html",
		"error.html",
		"login.html",
		"need_onboarding.html",
		"passphrase_reset.html",
		"passphrase_renew.html",
		"sharing_discovery.html",
	}
)

type renderer struct {
	t *template.Template
	h http.Handler
}

func (r *renderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	i := middlewares.GetInstance(c)
	t, err := r.t.Clone()
	if err != nil {
		return err
	}
	return t.Funcs(template.FuncMap{"t": i.Translate}).ExecuteTemplate(w, name, data)
}

func newRenderer(assetsPath string) (*renderer, error) {
	// By default, use the assets packed in the binary
	if assetsPath != "" {
		list := make([]string, len(templatesList))
		for i, name := range templatesList {
			list[i] = path.Join(assetsPath, "templates", name)
		}
		var err error
		t := template.New("stub").Funcs(template.FuncMap{"t": fmt.Sprintf})
		if t, err = t.ParseFiles(list...); err != nil {
			return nil, fmt.Errorf("Can't load the assets from %s", assetsPath)
		}
		h := http.FileServer(http.Dir(assetsPath))
		r := &renderer{t, h}
		return r, nil
	}

	statikFS, err := fs.New()
	if err != nil {
		return nil, err
	}

	var t, tmpl *template.Template
	for _, name := range templatesList {
		if t == nil {
			t = template.New(name)
			tmpl = t
		} else {
			tmpl = t.New(name)
		}
		tmpl = tmpl.Funcs(template.FuncMap{"t": fmt.Sprintf})
		f, err := statikFS.Open("/templates/" + name)
		if err != nil {
			return nil, fmt.Errorf("Can't load asset %s", name)
		}
		b, err := ioutil.ReadAll(f)
		if err != nil {
			return nil, err
		}
		if _, err = tmpl.Parse(string(b)); err != nil {
			return nil, err
		}
	}

	h := http.FileServer(statikFS)
	r := &renderer{t, h}
	return r, nil
}

// SetupAppsHandler adds all the necessary middlewares for the application
// handler.
func SetupAppsHandler(appsHandler echo.HandlerFunc) echo.HandlerFunc {
	mws := []echo.MiddlewareFunc{
		middlewares.LoadSession,
	}
	if !config.GetConfig().DisableCSP {
		secure := middlewares.Secure(&middlewares.SecureConfig{
			HSTSMaxAge:    hstsMaxAge,
			CSPDefaultSrc: []middlewares.CSPSource{middlewares.CSPSrcSelf, middlewares.CSPSrcParent, middlewares.CSPSrcWS},
			CSPStyleSrc:   []middlewares.CSPSource{middlewares.CSPSrcSelf, middlewares.CSPSrcParent, middlewares.CSPUnsafeInline},
			CSPFontSrc:    []middlewares.CSPSource{middlewares.CSPSrcSelf, middlewares.CSPSrcData, middlewares.CSPSrcParent},
			CSPImgSrc:     []middlewares.CSPSource{middlewares.CSPSrcSelf, middlewares.CSPSrcData, middlewares.CSPSrcBlob, middlewares.CSPSrcParent, middlewares.CSPSrcWhitelist},
			CSPFrameSrc:   []middlewares.CSPSource{middlewares.CSPSrcSiblings},
			XFrameOptions: middlewares.XFrameDeny,
		})
		mws = append([]echo.MiddlewareFunc{secure}, mws...)
	}

	return middlewares.Compose(appsHandler, mws...)
}

// SetupAssets add assets routing and handling to the given router. It also
// adds a Renderer to render templates.
func SetupAssets(router *echo.Echo, assetsPath string) error {
	r, err := newRenderer(assetsPath)
	if err != nil {
		return err
	}

	router.Renderer = r
	router.GET("/assets/*", echo.WrapHandler(http.StripPrefix("/assets/", r.h)))
	router.GET("/favicon.ico", echo.WrapHandler(r.h))
	router.GET("/robots.txt", echo.WrapHandler(r.h))
	return nil
}

// SetupRoutes sets the routing for HTTP endpoints
func SetupRoutes(router *echo.Echo) error {
	if !config.GetConfig().DisableCSP {
		secure := middlewares.Secure(&middlewares.SecureConfig{
			HSTSMaxAge:    hstsMaxAge,
			CSPDefaultSrc: []middlewares.CSPSource{middlewares.CSPSrcSelf},
			// Display logos of OAuth clients on the authorize page
			CSPImgSrc:     []middlewares.CSPSource{middlewares.CSPSrcAny},
			XFrameOptions: middlewares.XFrameDeny,
		})
		router.Use(secure)
	}

	router.Use(middlewares.CORS)

	mws := []echo.MiddlewareFunc{
		middlewares.NeedInstance,
		middlewares.LoadSession,
	}
	router.GET("/", auth.Home, mws...)
	// accounts routes does not all use middlewares
	konnectorsauth.Routes(router.Group("/accounts"))
	auth.Routes(router.Group("/auth", mws...))
	apps.WebappsRoutes(router.Group("/apps", mws...))
	apps.KonnectorRoutes(router.Group("/konnectors", mws...))
	registry.Routes(router.Group("/registry", mws...))
	data.Routes(router.Group("/data", mws...))
	if config.IsDevRelease() {
		search.Routes(router.Group("/search", mws...))
		imexport.Routes(router.Group("/imexport", mws...))
	}
	files.Routes(router.Group("/files", mws...))
	intents.Routes(router.Group("/intents", mws...))
	jobs.Routes(router.Group("/jobs", mws...))
	notifications.Routes(router.Group("/notifications", mws...))
	permissions.Routes(router.Group("/permissions", mws...))
	realtime.Routes(router.Group("/realtime", mws...))
	remote.Routes(router.Group("/remote", mws...))
	settings.Routes(router.Group("/settings", mws...))
	sharings.Routes(router.Group("/sharings", mws...))
	status.Routes(router.Group("/status"))
	version.Routes(router.Group("/version"))

	setupRecover(router)

	router.HTTPErrorHandler = errors.ErrorHandler
	return nil
}

// SetupAdminRoutes sets the routing for the administration HTTP endpoints
func SetupAdminRoutes(router *echo.Echo) error {
	if !config.IsDevRelease() {
		router.Use(middlewares.BasicAuth(config.AdminSecretFileName))
	}

	instances.Routes(router.Group("/instances"))
	version.Routes(router.Group("/version"))

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

	main.HTTPErrorHandler = errors.ErrorHandler
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
