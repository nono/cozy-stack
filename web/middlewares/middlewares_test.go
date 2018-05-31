package middlewares

import (
	"testing"

	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestSplitHost(t *testing.T) {
	config.UseTestFile()
	cfg := config.GetConfig()
	was := cfg.Subdomains
	defer func() { cfg.Subdomains = was }()

	host, app, siblings, ok := SplitHost("localhost")
	assert.Equal(t, "localhost", host)
	assert.Equal(t, "", app)
	assert.Equal(t, "", siblings)
	assert.True(t, ok)

	cfg.Subdomains = config.NestedSubdomains
	host, app, siblings, ok = SplitHost("calendar.joe.example.net")
	assert.Equal(t, "joe.example.net", host)
	assert.Equal(t, "calendar", app)
	assert.Equal(t, "*.joe.example.net", siblings)
	assert.True(t, ok)

	cfg.Subdomains = config.FlatSubdomains
	host, app, siblings, ok = SplitHost("joe-calendar.example.net")
	assert.Equal(t, "joe.example.net", host)
	assert.Equal(t, "calendar", app)
	assert.Equal(t, "*.example.net", siblings)
	assert.True(t, ok)

	host, app, siblings, ok = SplitHost("joe.example.net")
	assert.Equal(t, "joe.example.net", host)
	assert.Equal(t, "", app)
	assert.Equal(t, "", siblings)
	assert.True(t, ok)
}
