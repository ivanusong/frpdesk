package autostart

// 三种平台注册体的内容生成与「指向校验」纯函数（零系统调用）：
// 放在无 build tag 的共享文件，路径含空格/特殊字符/过期路径/损坏内容等关键风险
// 在任意平台的单元测试中全覆盖；平台文件只保留真实 I/O 薄层。

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// ---------- macOS launchd agent plist ----------

// launchdPlistXML 生成 launchd agent plist：RunAtLoad 让 launchd 在登录加载时
// 立即启动。exe 路径含空格无需转义（XML 文本节点原样保留，特殊字符按 XML 转义）。
func launchdPlistXML(exe string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("    <key>Label</key>\n    <string>" + AppID + "</string>\n")
	b.WriteString("    <key>ProgramArguments</key>\n    <array>\n")
	b.WriteString("        <string>" + xmlEscapeText(exe) + "</string>\n")
	b.WriteString("    </array>\n")
	b.WriteString("    <key>RunAtLoad</key>\n    <true/>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

// launchdPlistValues 浅解析 plist dict：key → 后续值元素（string 文本 / true 标记）
// 的顺序列表。plist 的 dict/key 相邻结构无法用 struct 标签表达，用 decoder 逐
// token 扫描；只需识别我们自己生成的简单形态（数组/嵌套层级自然穿透）。
// 返回 nil 表示文档损坏/截断：整体作废，绝不用部分已解析内容判定已注册。
func launchdPlistValues(raw []byte) map[string][]string {
	values := map[string][]string{}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	currentKey := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return values // 文档完整
			}
			return nil // 损坏/截断：不可信
		}
		elem, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch elem.Name.Local {
		case "key":
			var k string
			if dec.DecodeElement(&k, &elem) == nil {
				currentKey = k
			}
		case "string":
			var s string
			if dec.DecodeElement(&s, &elem) == nil && currentKey != "" {
				values[currentKey] = append(values[currentKey], s)
			}
		case "true":
			if currentKey != "" {
				values[currentKey] = append(values[currentKey], "true")
			}
		}
	}
}

// launchdPlistEnabled 判定已有 plist 是否表示「已注册且指向 exe」：
// Label 匹配 + RunAtLoad + 首个程序参数等于 exe（路径过期 = 未开启，重开会重写）。
// 损坏/截断/无法完整解析的内容一律按未注册处理（重新注册会整体重写）。
func launchdPlistEnabled(raw []byte, exe string) bool {
	v := launchdPlistValues(raw)
	if v == nil {
		return false
	}
	return len(v["ProgramArguments"]) > 0 && v["ProgramArguments"][0] == exe &&
		len(v["Label"]) > 0 && v["Label"][0] == AppID &&
		len(v["RunAtLoad"]) > 0
}

// xmlEscapeText XML 文本节点转义（路径可能出现 & < >）。
func xmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// ---------- Windows HKCU Run 注册表值 ----------

// windowsRunValue 生成 Run 注册表值：带双引号的完整路径（Program Files 等空格
// 路径不加引号会被系统拆解成错误命令行）。
func windowsRunValue(exe string) string {
	return `"` + exe + `"`
}

// windowsRunValueMatches 比对注册表现值是否已指向 exe：去首尾引号后按 Windows
// 路径惯例大小写不敏感比对（手装旧值可能无引号，指向相同程序即视为已注册）。
func windowsRunValueMatches(value, exe string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"`), exe)
}

// ---------- Linux/其他 XDG autostart 桌面项 ----------

// xdgExecEscape 按 Desktop Entry Spec 生成 Exec 值，共两层：
//  1. 参数引用层（spec quoted argument）：整体双引号包裹；双引号内必须转义
//     " ` $ \ 四字符；% 是字段码前缀，一律写作 %%；
//  2. key value 层（spec General String Escape）：对整个值内字面反斜杠再翻倍，
//     回车/换行/制表写作 \r \n \t。
//
// 因此路径中的字面反斜杠在文件里是连续 4 个反斜杠（两层各翻倍一次），
// 字面 $ 是 2 个反斜杠再 $。
func xdgExecEscape(exe string) string {
	// 参数引用层
	escaped := strings.ReplaceAll(exe, "%", "%%")
	escaped = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(escaped)
	quoted := `"` + escaped + `"`
	// key value 层：反斜杠翻倍 + 控制字符转义（对整个值生效）
	kv := strings.ReplaceAll(quoted, `\`, `\\`)
	kv = strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(kv)
	return kv
}

// xdgExecUnescape xdgExecEscape 的逆变换，严格逆序：
//  1. key value 层解码（单次扫描 \\ \n \r \t，输出不重复解析）；
//  2. 去外层双引号后做参数层解码（\\ \" \` \$）；
//  3. 折叠 %% 为 %。
//
// 非规范双引号形态按原样比对（含空格路径必然不匹配）；未知/残缺序列不解，
// 比对必然失败按未注册处理。
func xdgExecUnescape(value string) string {
	v := strings.NewReplacer(`\\`, `\`, `\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(strings.TrimSpace(value))
	if len(v) < 2 || !strings.HasPrefix(v, `"`) || !strings.HasSuffix(v, `"`) {
		return v
	}
	v = strings.NewReplacer(`\\`, `\`, `\"`, `"`, "\\`", "`", `\$`, `$`).Replace(v[1 : len(v)-1])
	return strings.ReplaceAll(v, "%%", "%")
}

// xdgDesktopEntry 生成 XDG autostart 桌面项：Exec 按桌面入口规范加引号转义，
// 空格/特殊字符路径安全；无 Terminal。
func xdgDesktopEntry(exe string) string {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Frpdesk\n")
	b.WriteString("Exec=" + xdgExecEscape(exe) + "\n")
	b.WriteString("Terminal=false\n")
	return b.String()
}

// xdgDesktopEntryEnabled 判定已有桌面项是否表示「已注册且指向 exe」：
// 仅认 [Desktop Entry] 组；Type 必须为 Application，Hidden=true 视为用户显式停用；
// Exec 去转义后精确比对（Linux 路径大小写敏感）。缺失 Exec/格式损坏按未注册处理。
func xdgDesktopEntryEnabled(raw []byte, exe string) bool {
	group := ""
	execValue := ""
	typeOK := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			group = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		if group != "Desktop Entry" {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Exec":
			execValue = strings.TrimSpace(value)
		case "Type":
			typeOK = strings.TrimSpace(value) == "Application"
		case "Hidden":
			if strings.TrimSpace(value) == "true" {
				return false
			}
		}
	}
	return typeOK && execValue != "" && xdgExecUnescape(execValue) == exe
}
