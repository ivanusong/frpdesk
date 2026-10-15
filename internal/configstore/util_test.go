package configstore

import "testing"

// TestBriefMessage 表驱动覆盖：多行折叠、超长截断（中英混合按 rune 计数）、
// 短串原样、max<=0 只单行化不截断。
func TestBriefMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{
			name: "多行与制表符折叠为单行",
			in:   "连接失败：\r\n\tdial tcp 1.2.3.4:7000\n        connect: connection refused",
			max:  0,
			want: "连接失败： dial tcp 1.2.3.4:7000 connect: connection refused",
		},
		{
			name: "超长截断（中英混合按 rune 计数）",
			in:   "错误abcdefghij",
			max:  5,
			want: "错误abc…",
		},
		{
			name: "短串原样",
			in:   "login success",
			max:  120,
			want: "login success",
		},
		{
			name: "max 为 0 只单行化不截断",
			in:   "a\nb\tc  d",
			max:  0,
			want: "a b c d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BriefMessage(tc.in, tc.max); got != tc.want {
				t.Fatalf("BriefMessage(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}
