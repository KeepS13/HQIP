package pgstore

import (
	"database/sql/driver"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Queries remain parameterized throughout adaptation. Values are never spliced
// into SQL, and the accepted dialect is confined to HQIP's static query corpus.
func translate(query string, args []driver.NamedValue) (string, error) {
	query = replaceSQL(query, "a.username=? COLLATE NOCASE", "lower(a.username)=lower(?)")
	var b strings.Builder
	arg := 0
	for i := 0; i < len(query); {
		if query[i] == '\'' || query[i] == '"' {
			j, err := quotedEnd(query, i)
			if err != nil {
				return "", err
			}
			b.WriteString(query[i:j])
			i = j
			continue
		}
		if query[i] == '?' {
			if arg >= len(args) {
				return "", errors.New("SQL parameter count mismatch")
			}
			b.WriteString("$" + strconv.Itoa(arg+1))
			switch args[arg].Value.(type) {
			case nil:
				b.WriteString("::text")
			case string:
				b.WriteString("::text")
			case int64:
				b.WriteString("::bigint")
			case float64:
				b.WriteString("::double precision")
			case []byte:
				b.WriteString("::bytea")
			case bool:
				b.WriteString("::bigint")
			}
			arg++
			i++
			continue
		}
		b.WriteByte(query[i])
		i++
	}
	if arg != len(args) {
		return "", errors.New("SQL parameter count mismatch")
	}
	q, err := functions(b.String())
	if err != nil {
		return "", err
	}
	q = strings.TrimSpace(q)
	if strings.HasPrefix(strings.ToUpper(q), "INSERT OR IGNORE ") {
		q = "INSERT " + q[len("INSERT OR IGNORE "):] + " ON CONFLICT DO NOTHING"
	}
	q = replaceSQL(q, "NOT GLOB '__probe_*'", "NOT LIKE '\\_\\_probe\\_%' ESCAPE '\\'")
	// Only this legacy account lookup uses NOCASE; usernames are normalized on write.
	q = replaceSQL(q, " COLLATE NOCASE", "")
	q = qualifyConflict(q)
	return q, nil
}

func quotedEnd(s string, start int) (int, error) {
	quote := s[start]
	for i := start + 1; i < len(s); i++ {
		if s[i] == quote {
			if i+1 < len(s) && s[i+1] == quote {
				i++
				continue
			}
			return i + 1, nil
		}
	}
	return 0, errors.New("unterminated SQL literal")
}
func ident(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
func splitArgs(s string) ([]string, error) {
	var parts []string
	level, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			j, e := quotedEnd(s, i)
			if e != nil {
				return nil, e
			}
			i = j - 1
		case '(':
			level++
		case ')':
			level--
		case ',':
			if level == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if level != 0 {
		return nil, errors.New("unbalanced SQL expression")
	}
	return append(parts, strings.TrimSpace(s[start:])), nil
}
func functions(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\'' || s[i] == '"' {
			j, e := quotedEnd(s, i)
			if e != nil {
				return "", e
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if !ident(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		start := i
		for i < len(s) && ident(s[i]) {
			i++
		}
		name := s[start:i]
		j := i
		for j < len(s) && s[j] == ' ' {
			j++
		}
		if j >= len(s) || s[j] != '(' {
			if name == "day" || name == "end" {
				b.WriteString(`"` + name + `"`)
			} else {
				b.WriteString(name)
			}
			continue
		}
		level, k := 1, j+1
		for ; k < len(s) && level > 0; k++ {
			if s[k] == '\'' || s[k] == '"' {
				end, e := quotedEnd(s, k)
				if e != nil {
					return "", e
				}
				k = end - 1
				continue
			}
			if s[k] == '(' {
				level++
			}
			if s[k] == ')' {
				level--
			}
		}
		if level != 0 {
			return "", errors.New("unbalanced SQL function")
		}
		body, e := functions(s[j+1 : k-1])
		if e != nil {
			return "", e
		}
		a, e := splitArgs(body)
		if e != nil {
			return "", e
		}
		expr := name + "(" + body + ")"
		switch strings.ToLower(name) {
		case "json_extract", "json_each":
			if len(a) != 2 || !regexp.MustCompile(`^'\$\.[A-Za-z0-9_]+'$`).MatchString(a[1]) {
				return "", errors.New("unsupported JSON path")
			}
			key := a[1][3 : len(a[1])-1]
			expr = "(" + a[0] + "::jsonb->>'" + key + "')"
			if strings.EqualFold(name, "json_each") {
				expr = "jsonb_array_elements_text(" + a[0] + "::jsonb->'" + key + "') AS json_items(value)"
			} else {
				switch key {
				case "port", "authPort", "statsPort", "sshPort", "revision", "version", "processedConfigRevision", "appliedConfigRevision", "monthlyQuotaBytes":
					expr = "(" + expr + ")::bigint"
				case "bandwidthMbps":
					expr = "(" + expr + ")::double precision"
				}
			}
		case "json":
			if len(a) != 1 {
				return "", errors.New("unsupported JSON expression")
			}
			expr = "(" + a[0] + ")::jsonb"
		case "json_set":
			if len(a) < 3 || len(a)%2 != 1 {
				return "", errors.New("unsupported JSON update")
			}
			expr = a[0] + "::jsonb"
			for n := 1; n < len(a); n += 2 {
				if !regexp.MustCompile(`^'\$\.[A-Za-z0-9_]+'$`).MatchString(a[n]) {
					return "", errors.New("unsupported JSON update path")
				}
				v := a[n+1]
				if strings.HasPrefix(v, "'") {
					v += "::text"
				}
				if strings.EqualFold(v, "NULL") {
					v = "NULL::text"
				}
				expr = "jsonb_set(" + expr + ",'{" + a[n][3:len(a[n])-1] + "}',COALESCE(to_jsonb(" + v + "),'null'::jsonb),true)"
			}
			expr = "(" + expr + ")::text"
		case "json_remove":
			expr = a[0] + "::jsonb"
			for _, path := range a[1:] {
				if !regexp.MustCompile(`^'\$\.[A-Za-z0-9_]+'$`).MatchString(path) {
					return "", errors.New("unsupported JSON removal")
				}
				expr += " - '" + path[3:len(path)-1] + "'"
			}
			expr = "(" + expr + ")::text"
		case "max":
			if len(a) > 1 {
				expr = "GREATEST(" + body + ")"
			}
		case "min":
			if len(a) > 1 {
				expr = "LEAST(" + body + ")"
			}
		case "sum":
			if len(a) == 1 && !strings.Contains(strings.ToUpper(body), "CASE ") && (strings.Contains(body, "=") || strings.Contains(strings.ToUpper(body), " IN ")) {
				expr = "SUM((" + body + ")::integer)"
			}
		case "instr":
			if len(a) == 2 {
				expr = "strpos(" + a[0] + "," + a[1] + ")"
			}
		}
		b.WriteString(expr)
		i = k
	}
	return b.String(), nil
}

var insertTable = regexp.MustCompile(`(?i)^INSERT\s+INTO\s+([a-z_]+)\s*\(`)

// PostgreSQL requires target columns in an UPSERT expression to be qualified.
func qualifyConflict(q string) string {
	m := insertTable.FindStringSubmatch(q)
	pos := strings.Index(strings.ToUpper(q), "DO UPDATE SET ")
	if len(m) != 2 || pos < 0 {
		return q
	}
	start := pos + len("DO UPDATE SET ")
	tail := q[start:]
	var b strings.Builder
	for i := 0; i < len(tail); {
		if tail[i] == '\'' || tail[i] == '"' {
			j, e := quotedEnd(tail, i)
			if e != nil {
				return q
			}
			b.WriteString(tail[i:j])
			i = j
			continue
		}
		if !ident(tail[i]) {
			b.WriteByte(tail[i])
			i++
			continue
		}
		s := i
		for i < len(tail) && ident(tail[i]) {
			i++
		}
		word := tail[s:i]
		j := i
		for j < len(tail) && tail[j] == ' ' {
			j++
		}
		if columnNames[m[1]][word] && (s == 0 || tail[s-1] != '.') && (j == len(tail) || tail[j] != '=') {
			b.WriteString(m[1] + ".")
		}
		b.WriteString(word)
	}
	return q[:start] + b.String()
}

func replaceSQL(s, old, replacement string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\'' || s[i] == '"' {
			end, err := quotedEnd(s, i)
			if err != nil {
				return s
			}
			b.WriteString(s[i:end])
			i = end
		} else if strings.HasPrefix(s[i:], old) {
			b.WriteString(replacement)
			i += len(old)
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}
