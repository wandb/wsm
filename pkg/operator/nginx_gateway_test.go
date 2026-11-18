package operator

import "testing"

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/nginx/nginx-gateway-fabric:2.5.1":                       "2.5.1",
		"local-registry:5000/nginx/nginx-gateway-fabric:2.9.0":           "2.9.0",
		"local-registry:5000/nginx/nginx-gateway-fabric":                 "",
		"ghcr.io/nginx/nginx-gateway-fabric:2.9.0@sha256:0123456789abcd": "2.9.0",
		"ghcr.io/nginx/nginx-gateway-fabric@sha256:0123456789abcd":       "",
	}
	for image, want := range cases {
		if got := imageTag(image); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestVersionOlder(t *testing.T) {
	cases := []struct {
		have, want string
		older      bool
	}{
		{"v1.4.0", GatewayAPIVersion, true},
		{"v1.7.0", GatewayAPIVersion, false},
		{"v1.8.0", GatewayAPIVersion, false},
		{"", GatewayAPIVersion, false},
		{"2.5.1", NginxGatewayVersion, true},
		{NginxGatewayVersion, NginxGatewayVersion, false},
		{"edge", NginxGatewayVersion, false},
	}
	for _, c := range cases {
		if got := versionOlder(c.have, c.want); got != c.older {
			t.Errorf("versionOlder(%q, %q) = %v, want %v", c.have, c.want, got, c.older)
		}
	}
}
