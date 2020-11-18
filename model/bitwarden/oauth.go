package bitwarden

import (
	"strconv"
	"strings"

	"github.com/cozy/cozy-stack/model/bitwarden/settings"
	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/oauth"
	"github.com/cozy/cozy-stack/model/permission"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/crypto"
	jwt "gopkg.in/dgrijalva/jwt-go.v3"
)

// BitwardenScope is the OAuth scope, and it is hard-coded with the doctypes
// needed by the Bitwarden apps.
var BitwardenScope = strings.Join([]string{
	consts.BitwardenProfiles,
	consts.BitwardenCiphers,
	consts.BitwardenFolders,
	consts.BitwardenOrganizations,
	consts.Konnectors,
	consts.AppsSuggestion,
	consts.Support,
}, " ")

// ParseBitwardenDeviceType takes a deviceType (Bitwarden) and transforms it
// into a client_kind and a software_id (Cozy).
// See https://github.com/bitwarden/server/blob/f37f33512046707eef69a2cb3944338de819439d/src/Core/Enums/DeviceType.cs
func ParseBitwardenDeviceType(deviceType string) (string, string) {
	device, err := strconv.Atoi(deviceType)
	if err == nil {
		switch device {
		case 0, 1, 15, 16:
			// 0 = Android
			// 1 = iOS
			// 15 = Android (amazon variant)
			// 16 = UWP
			return "mobile", "github.com/bitwarden/mobile"
		case 5, 6, 7:
			// 5 = Windows
			// 6 = macOS
			// 7 = Linux
			return "desktop", "github.com/bitwarden/desktop"
		case 2, 3, 4, 19, 20:
			// 2 = Chrome extension
			// 3 = Firefox extension
			// 4 = Edge extension
			// 19 = Vivaldi extension
			// 20 = Safari extension
			return "browser", "github.com/bitwarden/browser"
		case 8, 9, 10, 11, 12, 13, 14, 17, 18:
			// 8 = Chrome
			// 9 = Firefox
			// 10 = Opera
			// 11 = Edge
			// 12 = Internet Explorer
			// 13 = Unknown browser
			// 17 = Safari
			// 18 = Vivaldi
			return "web", "github.com/bitwarden/web"
		}
	}
	return "unknown", "github.com/bitwarden"
}

type bitwardenClaims struct {
	permission.Claims
	Name     string `json:"name"`
	Email    string `json:"email"`
	Verified bool   `json:"email_verified"`
	Premium  bool   `json:"premium"`
}

// CreateAccessJWT returns a new JSON Web Token that can be used with Bitwarden
// apps. It is an access token, with some additional custom fields.
// See https://github.com/bitwarden/jslib/blob/67b2b5318556f2d21bf4f2d117af8228b9f9549c/src/services/token.service.ts
func CreateAccessJWT(i *instance.Instance, c *oauth.Client) (string, error) {
	now := crypto.Timestamp()
	name, err := i.SettingsPublicName()
	if err != nil {
		name = "Anonymous"
	}
	var stamp string
	if settings, err := settings.Get(i); err == nil {
		stamp = settings.SecurityStamp
	}
	token, err := crypto.NewJWT(i.OAuthSecret, bitwardenClaims{
		Claims: permission.Claims{
			StandardClaims: jwt.StandardClaims{
				Audience:  consts.AccessTokenAudience,
				Issuer:    i.Domain,
				NotBefore: now - 60,
				IssuedAt:  now,
				ExpiresAt: now + int64(consts.AccessTokenValidityDuration.Seconds()),
				Subject:   c.CouchID,
			},
			SStamp: stamp,
			Scope:  BitwardenScope,
		},
		Name:     name,
		Email:    string(i.PassphraseSalt()),
		Verified: false,
		Premium:  false,
	})
	if err != nil {
		i.Logger().WithField("nspace", "oauth").
			Errorf("Failed to create the bitwarden access token: %s", err)
	}
	return token, err
}

// CreateRefreshJWT returns a new JSON Web Token that can be used with
// Bitwarden apps. It is a refresh token, with an additional security stamp.
func CreateRefreshJWT(i *instance.Instance, c *oauth.Client) (string, error) {
	var stamp string
	if settings, err := settings.Get(i); err == nil {
		stamp = settings.SecurityStamp
	}
	token, err := crypto.NewJWT(i.OAuthSecret, permission.Claims{
		StandardClaims: jwt.StandardClaims{
			Audience: consts.RefreshTokenAudience,
			Issuer:   i.Domain,
			IssuedAt: crypto.Timestamp(),
			Subject:  c.CouchID,
		},
		SStamp: stamp,
		Scope:  BitwardenScope,
	})
	if err != nil {
		i.Logger().WithField("nspace", "oauth").
			Errorf("Failed to create the bitwarden refresh token: %s", err)
	}
	return token, err
}
