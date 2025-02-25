/*
  Caddy v2 module to filter requests based on source IP geographic location. This was a feature provided by the V1 ipfilter middleware.
  Complete documentation and usage examples are available at https://github.com/JamesShiu/caddy-maxmind-geolocation
*/
package caddy_maxmind_geolocation

import (
	"fmt"
	"net"
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/ip2location/ip2location-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Interface guards
var (
	_ caddy.Module             = (*MaxmindGeolocation)(nil)
	_ caddyhttp.RequestMatcher = (*MaxmindGeolocation)(nil)
	_ caddy.Provisioner        = (*MaxmindGeolocation)(nil)
	_ caddy.CleanerUpper       = (*MaxmindGeolocation)(nil)
	_ caddyfile.Unmarshaler    = (*MaxmindGeolocation)(nil)
)

func init() {
	caddy.RegisterModule(MaxmindGeolocation{})
}

// Allows to filter requests based on source IP country.
type MaxmindGeolocation struct {

	// The path of the ip2location db file.
	DbPath string `json:"db_path"`

	// The path of the zap log
	LogPath string `json:"log_path"`

	// A list of countries that the filter will allow.
	// If you specify this, you should not specify DenyCountries.
	// If both are specified, DenyCountries will take precedence.
	// All countries that are not in this list will be denied.
	// You can specify the special value "UNK" to match unrecognized countries.
	AllowCountries []string `json:"allow_countries"`

	// A list of countries that the filter will deny.
	// If you specify this, you should not specify AllowCountries.
	// If both are specified, DenyCountries will take precedence.
	// All countries that are not in this list will be allowed.
	// You can specify the special value "UNK" to match unrecognized countries.
	DenyCountries []string `json:"deny_countries"`

	dbInst *ip2location.DB
	logger *zap.Logger
}

/*
	The matcher configuration will have a single block with the following parameters:

	- `db_path`: required, is the path to the GeoLite2-Country.mmdb file

	- `allow_countries`: a space-separated list of allowed countries

	- `deny_countries`: a space-separated list of denied countries.

	You will want specify just one of `allow_countries` or `deny_countries`. If you
	specify both of them, denied countries will take precedence over allowed ones.
	If you specify none of them, all requests will be denied.

	Examples are available at https://github.com/JamesShiu/caddy-maxmind-geolocation/
*/
func (m *MaxmindGeolocation) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	current := 0
	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			switch d.Val() {
			case "db_path":
				current = 1
			case "allow_countries":
				current = 2
			case "deny_countries":
				current = 3
			case "log_path":
				current = 4
			default:
				switch current {
				case 1:
					m.DbPath = d.Val()
					current = 0
				case 2:
					m.AllowCountries = append(m.AllowCountries, d.Val())
				case 3:
					m.DenyCountries = append(m.DenyCountries, d.Val())
				case 4:
					m.LogPath = d.Val()
					current = 0
				default:
					return fmt.Errorf("unexpected config parameter %s", d.Val())
				}
			}
		}
	}
	return nil
}

func (MaxmindGeolocation) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.matchers.maxmind_geolocation",
		New: func() caddy.Module { return new(MaxmindGeolocation) },
	}
}

func (m *MaxmindGeolocation) Provision(ctx caddy.Context) error {
	var err error
	if m.LogPath != "" {
		m.logger, err = NewLogger(m.LogPath)
		if err != nil {
			return fmt.Errorf("cannot open log file %s: %v", m.LogPath, err)
		}
	}
	m.dbInst, err = ip2location.OpenDB(m.DbPath)
	if err != nil {
		return fmt.Errorf("cannot open database file %s: %v", m.DbPath, err)
	}
	return nil
}

func (m *MaxmindGeolocation) Cleanup() error {
	if m.dbInst != nil {
		m.dbInst.Close()
	}
	return nil
}

func NewLogger(logPath string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.OutputPaths = []string{
		logPath,
	}
	cfg.Level.SetLevel(zapcore.DebugLevel)
	cfg.DisableCaller = true
	return cfg.Build()
}

func (m *MaxmindGeolocation) checkAllowed(item string, allowedList []string, deniedList []string) bool {
	if item == "" || item == "0" {
		item = "UNK"
	}
	if len(deniedList) > 0 {
		for _, i := range deniedList {
			if i == item {
				return false
			}
		}
		return true
	}
	if len(allowedList) > 0 {
		for _, i := range allowedList {
			if i == item {
				return true
			}
		}
		return false
	}
	return true
}

func (m *MaxmindGeolocation) Match(r *http.Request) bool {

	// If both the allow and deny fields are empty, let the request pass
	if len(m.AllowCountries) < 1 && len(m.DenyCountries) < 1 {
		return true
	}

	remoteIp, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("cannot split IP address", zap.String("address", r.RemoteAddr), zap.Error(err))
		}
	}

	// Get the record from the database
	addr := net.ParseIP(remoteIp)
	if addr == nil {
		if m.logger != nil {
			m.logger.Warn("cannot parse IP address", zap.String("address", r.RemoteAddr))
		}
		return false
	}
	var record ip2location.IP2Locationrecord
	record, err = m.dbInst.Get_country_short(addr.String())
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("cannot lookup IP address", zap.String("address", r.RemoteAddr), zap.Error(err))
		}
		return false
	}

	if m.logger != nil {
		m.logger.Debug(
			"Detected ip2location data",
			zap.String("ip", r.RemoteAddr),
			zap.String("country", record.Country_short),
		)
	}

	if !m.checkAllowed(record.Country_short, m.AllowCountries, m.DenyCountries) {
		if m.logger != nil {
			m.logger.Debug("Country not allowed", zap.String("country", record.Country_short))
		}
		return false
	}

	return true
}
