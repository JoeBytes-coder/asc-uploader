package uploader

import "testing"

func TestNormalizeKeyType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", KeyTypeTeam},
		{"team", KeyTypeTeam},
		{"TEAM", KeyTypeTeam},
		{"individual", KeyTypeIndividual},
		{"Individual", KeyTypeIndividual},
		{"unknown-value", KeyTypeTeam}, // 未知值一律按团队密钥
	}
	for _, c := range cases {
		if got := NormalizeKeyType(c.in); got != c.want {
			t.Errorf("NormalizeKeyType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKeyTypeLabel(t *testing.T) {
	if KeyTypeLabel("") == "" {
		t.Error("团队密钥说明不应为空")
	}
	if KeyTypeLabel("individual") == KeyTypeLabel("team") {
		t.Error("个人/团队密钥说明应不同")
	}
}
