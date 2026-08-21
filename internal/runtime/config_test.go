package runtime

import "testing"

func TestAddressPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		port    string
		want    string
		wantErr bool
	}{{"默认", nil, "", DefaultAddress, false}, {"环境端口", nil, "24567", "127.0.0.1:24567", false}, {"显式优先", []string{"-addr=127.0.0.1:25432"}, "24567", "127.0.0.1:25432", false}, {"禁止通配", []string{"-addr=0.0.0.0:19463"}, "", "", true}, {"禁止低位", []string{"-addr=127.0.0.1:80"}, "", "", true}, {"环境非法", nil, "8080", "", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(tt.args, func(string) string { return tt.port })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err == nil && cfg.Address != tt.want {
				t.Fatalf("got %q want %q", cfg.Address, tt.want)
			}
		})
	}
}
