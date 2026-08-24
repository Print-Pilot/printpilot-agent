package cli

import (
	"os"
	"regexp"
	"testing"
)

func TestNewPrinterID(t *testing.T) {
	cases := []string{"Taller 2", "taller-2", "raspberrypi", "  P1! ", "imprimidora-con-nombre-largo-que-supera-los-limites-normales"}
	for _, c := range cases {
		id := newPrinterID(c)
		if !regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*-[0-9a-f]{6}$`).MatchString(id) {
			t.Errorf("newPrinterID(%q) = %q, no parece un printer_id válido", c, id)
		}
	}
}

func TestNewPrinterIDUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := newPrinterID("misma-máquina")
		if seen[id] {
			t.Fatalf("id duplicado: %s", id)
		}
		seen[id] = true
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Taller 2":      "taller-2",
		"Mi Impresora!": "mi-impresora",
		"  P1  ":        "p1",
		"---":           "printer",
		"Café":          "caf",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskToken(t *testing.T) {
	if got := maskToken(""); got != "(vacío)" {
		t.Errorf("maskToken vacío = %q", got)
	}
	if got := maskToken("abcdef123456"); got != "abcdef******" {
		t.Errorf("maskToken = %q", got)
	}
	if got := maskToken("abc"); got != "***" {
		t.Errorf("maskToken corto = %q", got)
	}
}

func TestParseSlogLine(t *testing.T) {
	line := `time=2026-08-24T12:34:56-03:00 level=INFO msg="conectado al hub" url=wss://hub.example.com/ws`
	ts, level, msg := parseSlogLine(line)
	if ts != "2026-08-24T12:34:56-03:00" {
		t.Errorf("ts = %q", ts)
	}
	if level != "INFO" {
		t.Errorf("level = %q", level)
	}
	if msg != "conectado al hub" {
		t.Errorf("msg = %q, want %q", msg, "conectado al hub")
	}

	// msg sin comillas (solo una palabra).
	_, _, msg = parseSlogLine(`time=... level=INFO msg=reintentando`)
	if msg != "reintentando" {
		t.Errorf("msg unquoted = %q", msg)
	}
}

func TestHubState(t *testing.T) {
	logFile := t.TempDir() + "/agent.log"
	writeLog := func(lines []string) {
		data := ""
		for _, l := range lines {
			data += l + "\n"
		}
		if err := os.WriteFile(logFile, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Conectado reciente gana aunque antes hubo fallos.
	writeLog([]string{
		`time=2026-08-24T09:00:00-03:00 level=WARN msg="conexión perdida"`,
		`time=2026-08-24T09:00:30-03:00 level=INFO msg="conectado al hub" url=ws://h`,
	})
	if got := hubState(logFile).state; got != "connected" {
		t.Errorf("state = %q, want connected", got)
	}

	// Rechazo de auth es prioritario.
	writeLog([]string{
		`time=2026-08-24T09:00:00-03:00 level=INFO msg="conectado al hub" url=ws://h`,
		`time=2026-08-24T09:01:00-03:00 level=ERROR msg="autenticación rechazada por el hub; revisar token/printer_id. Reintentando cada 60s"`,
	})
	if got := hubState(logFile).state; got != "auth-rejected" {
		t.Errorf("state = %q, want auth-rejected", got)
	}
}
