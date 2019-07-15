package provisioner

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/smallstep/assert"
)

func TestGCP_Getters(t *testing.T) {
	p, err := generateGCP()
	assert.FatalError(t, err)
	id := "gcp/" + p.Name
	if got := p.GetID(); got != id {
		t.Errorf("GCP.GetID() = %v, want %v", got, id)
	}
	if got := p.GetName(); got != p.Name {
		t.Errorf("GCP.GetName() = %v, want %v", got, p.Name)
	}
	if got := p.GetType(); got != TypeGCP {
		t.Errorf("GCP.GetType() = %v, want %v", got, TypeGCP)
	}
	kid, key, ok := p.GetEncryptedKey()
	if kid != "" || key != "" || ok == true {
		t.Errorf("GCP.GetEncryptedKey() = (%v, %v, %v), want (%v, %v, %v)",
			kid, key, ok, "", "", false)
	}

	aud := "https://ca.smallstep.com/1.0/sign#" + url.QueryEscape(id)
	expected := fmt.Sprintf("http://metadata/computeMetadata/v1/instance/service-accounts/default/identity?audience=%s&format=full&licenses=FALSE", url.QueryEscape(aud))
	if got := p.GetIdentityURL(aud); got != expected {
		t.Errorf("GCP.GetIdentityURL() = %v, want %v", got, expected)
	}
}

func TestGCP_GetTokenID(t *testing.T) {
	p1, err := generateGCP()
	assert.FatalError(t, err)
	p1.Name = "name"

	p2, err := generateGCP()
	assert.FatalError(t, err)
	p2.DisableTrustOnFirstUse = true

	now := time.Now()
	t1, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", "gcp/name",
		"instance-id", "instance-name", "project-id", "zone",
		now, &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	t2, err := generateGCPToken(p2.ServiceAccounts[0],
		"https://accounts.google.com", p2.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		now, &p2.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)

	sum := sha256.Sum256([]byte("gcp/name.instance-id"))
	want1 := strings.ToLower(hex.EncodeToString(sum[:]))
	sum = sha256.Sum256([]byte(t2))
	want2 := strings.ToLower(hex.EncodeToString(sum[:]))

	type args struct {
		token string
	}
	tests := []struct {
		name    string
		gcp     *GCP
		args    args
		want    string
		wantErr bool
	}{
		{"ok", p1, args{t1}, want1, false},
		{"ok", p2, args{t2}, want2, false},
		{"fail token", p1, args{"token"}, "", true},
		{"fail claims", p1, args{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.ey.fooo"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.gcp.GetTokenID(tt.args.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("GCP.GetTokenID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GCP.GetTokenID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGCP_GetIdentityToken(t *testing.T) {
	p1, err := generateGCP()
	assert.FatalError(t, err)

	t1, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad-request":
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		default:
			w.Write([]byte(t1))
		}
	}))
	defer srv.Close()

	type args struct {
		subject string
		caURL   string
	}
	tests := []struct {
		name        string
		gcp         *GCP
		args        args
		identityURL string
		want        string
		wantErr     bool
	}{
		{"ok", p1, args{"subject", "https://ca"}, srv.URL, t1, false},
		{"fail ca url", p1, args{"subject", "://ca"}, srv.URL, "", true},
		{"fail request", p1, args{"subject", "https://ca"}, srv.URL + "/bad-request", "", true},
		{"fail url", p1, args{"subject", "https://ca"}, "://ca.smallstep.com", "", true},
		{"fail connect", p1, args{"subject", "https://ca"}, "foobarzar", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.gcp.config.IdentityURL = tt.identityURL
			got, err := tt.gcp.GetIdentityToken(tt.args.subject, tt.args.caURL)
			t.Log(err)
			if (err != nil) != tt.wantErr {
				t.Errorf("GCP.GetIdentityToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GCP.GetIdentityToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGCP_Init(t *testing.T) {
	srv := generateJWKServer(2)
	defer srv.Close()

	config := Config{
		Claims: globalProvisionerClaims,
	}
	badClaims := &Claims{
		DefaultTLSDur: &Duration{0},
	}
	zero := Duration{Duration: 0}
	type fields struct {
		Type            string
		Name            string
		ServiceAccounts []string
		InstanceAge     Duration
		Claims          *Claims
	}
	type args struct {
		config   Config
		certsURL string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{"ok", fields{"GCP", "name", nil, zero, nil}, args{config, srv.URL}, false},
		{"ok", fields{"GCP", "name", []string{"service-account"}, zero, nil}, args{config, srv.URL}, false},
		{"ok", fields{"GCP", "name", []string{"service-account"}, Duration{Duration: 1 * time.Minute}, nil}, args{config, srv.URL}, false},
		{"bad type", fields{"", "name", nil, zero, nil}, args{config, srv.URL}, true},
		{"bad name", fields{"GCP", "", nil, zero, nil}, args{config, srv.URL}, true},
		{"bad duration", fields{"GCP", "name", nil, Duration{Duration: -1 * time.Minute}, nil}, args{config, srv.URL}, true},
		{"bad claims", fields{"GCP", "name", nil, zero, badClaims}, args{config, srv.URL}, true},
		{"bad certs", fields{"GCP", "name", nil, zero, nil}, args{config, srv.URL + "/error"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &GCP{
				Type:            tt.fields.Type,
				Name:            tt.fields.Name,
				ServiceAccounts: tt.fields.ServiceAccounts,
				InstanceAge:     tt.fields.InstanceAge,
				Claims:          tt.fields.Claims,
				config: &gcpConfig{
					CertsURL:    tt.args.certsURL,
					IdentityURL: gcpIdentityURL,
				},
			}
			if err := p.Init(tt.args.config); (err != nil) != tt.wantErr {
				t.Errorf("GCP.Init() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGCP_AuthorizeSign(t *testing.T) {
	p1, err := generateGCP()
	assert.FatalError(t, err)

	p2, err := generateGCP()
	assert.FatalError(t, err)
	p2.DisableCustomSANs = true

	p3, err := generateGCP()
	assert.FatalError(t, err)
	p3.ProjectIDs = []string{"other-project-id"}
	p3.ServiceAccounts = []string{"foo@developer.gserviceaccount.com"}
	p3.InstanceAge = Duration{1 * time.Minute}

	aKey, err := generateJSONWebKey()
	assert.FatalError(t, err)

	t1, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	t2, err := generateGCPToken(p2.ServiceAccounts[0],
		"https://accounts.google.com", p2.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p2.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	t3, err := generateGCPToken(p3.ServiceAccounts[0],
		"https://accounts.google.com", p3.GetID(),
		"instance-id", "instance-name", "other-project-id", "zone",
		time.Now(), &p3.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)

	failKey, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), aKey)
	assert.FatalError(t, err)
	failIss, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://foo.bar.zar", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failAud, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", "gcp:foo",
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failExp, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now().Add(-360*time.Second), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failNbf, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now().Add(360*time.Second), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failServiceAccount, err := generateGCPToken("foo",
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failInvalidProjectID, err := generateGCPToken(p3.ServiceAccounts[0],
		"https://accounts.google.com", p3.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p3.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failInvalidInstanceAge, err := generateGCPToken(p3.ServiceAccounts[0],
		"https://accounts.google.com", p3.GetID(),
		"instance-id", "instance-name", "other-project-id", "zone",
		time.Now().Add(-1*time.Minute), &p3.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failInstanceID, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failInstanceName, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failProjectID, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)
	failZone, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)

	type args struct {
		token string
	}
	tests := []struct {
		name    string
		gcp     *GCP
		args    args
		wantLen int
		wantErr bool
	}{
		{"ok", p1, args{t1}, 3, false},
		{"ok", p2, args{t2}, 5, false},
		{"ok", p3, args{t3}, 3, false},
		{"fail token", p1, args{"token"}, 0, true},
		{"fail key", p1, args{failKey}, 0, true},
		{"fail iss", p1, args{failIss}, 0, true},
		{"fail aud", p1, args{failAud}, 0, true},
		{"fail exp", p1, args{failExp}, 0, true},
		{"fail nbf", p1, args{failNbf}, 0, true},
		{"fail service account", p1, args{failServiceAccount}, 0, true},
		{"fail invalid project id", p3, args{failInvalidProjectID}, 0, true},
		{"fail invalid instance age", p3, args{failInvalidInstanceAge}, 0, true},
		{"fail instance id", p1, args{failInstanceID}, 0, true},
		{"fail instance name", p1, args{failInstanceName}, 0, true},
		{"fail project id", p1, args{failProjectID}, 0, true},
		{"fail zone", p1, args{failZone}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.gcp.AuthorizeSign(tt.args.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("GCP.AuthorizeSign() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Len(t, tt.wantLen, got)
		})
	}
}

func TestGCP_AuthorizeRenewal(t *testing.T) {
	p1, err := generateGCP()
	assert.FatalError(t, err)
	p2, err := generateGCP()
	assert.FatalError(t, err)

	// disable renewal
	disable := true
	p2.Claims = &Claims{DisableRenewal: &disable}
	p2.claimer, err = NewClaimer(p2.Claims, globalProvisionerClaims)
	assert.FatalError(t, err)

	type args struct {
		cert *x509.Certificate
	}
	tests := []struct {
		name    string
		prov    *GCP
		args    args
		wantErr bool
	}{
		{"ok", p1, args{nil}, false},
		{"fail", p2, args{nil}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.prov.AuthorizeRenewal(tt.args.cert); (err != nil) != tt.wantErr {
				t.Errorf("GCP.AuthorizeRenewal() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGCP_AuthorizeRevoke(t *testing.T) {
	p1, err := generateGCP()
	assert.FatalError(t, err)

	t1, err := generateGCPToken(p1.ServiceAccounts[0],
		"https://accounts.google.com", p1.GetID(),
		"instance-id", "instance-name", "project-id", "zone",
		time.Now(), &p1.keyStore.keySet.Keys[0])
	assert.FatalError(t, err)

	type args struct {
		token string
	}
	tests := []struct {
		name    string
		gcp     *GCP
		args    args
		wantErr bool
	}{
		{"ok", p1, args{t1}, true}, // revoke is disabled
		{"fail", p1, args{"token"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.gcp.AuthorizeRevoke(tt.args.token); (err != nil) != tt.wantErr {
				t.Errorf("GCP.AuthorizeRevoke() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
