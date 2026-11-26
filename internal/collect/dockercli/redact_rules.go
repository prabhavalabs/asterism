package dockercli

import (
	"regexp"
	"slices"
	"strings"
)

// This file holds the shared redaction rules for free text (RedactText),
// argument vectors (RedactArgs) and argument vectors that may contain shell
// strings (RedactShellArgs). They are applied by the SSH bridge, by the
// agent before it encodes a frame, and again on the hub for anything an
// agent reports, so every rule must be idempotent: redacting redacted
// output changes nothing.
//
// Rules, in order of precedence for each argument:
//
//  1. Program-aware short and long options for well-known clients, active
//     after the program's name appears (argv[0], or a later word such as
//     "exec redis-cli" or "sshpass -p x mysql"):
//     redis-cli/valkey-cli -a PW, mysql/mariadb tools -pPW and -p PW,
//     mongosh/mongo tools -p PW, curl -u/-U/-E/--user/--proxy-user USER:PW
//     (only the password part) and -b/--cookie DATA, sshpass -p PW, docker/podman/nerdctl login
//     -p PW. PostgreSQL tools and wget are listed so that their -p (port,
//     page-requisites) is never treated as a password.
//  2. Generic long options whose name looks secret (--password X,
//     --token=X, -password X), except names that carry no secret value
//     (--password-file, --password-stdin, --no-auth, --auth-source, ...).
//  3. KEY=VALUE words and option values (-e DB_PASSWORD=x,
//     --env=API_TOKEN=x, -Ddb.password=x) whose key looks secret.
//  4. Free-text rules on everything else: URL userinfo, Authorization and
//     Cookie headers, Bearer tokens and key=value / key: value pairs.
//
// A value that is a plain variable reference ($PW, ${PW}) is kept: it names
// the secret without revealing it.

var (
	// urlPassRe matches scheme://user:password@ (user may be empty). The
	// password is greedy up to the last '@' of the authority, so a
	// password containing '@' is redacted whole.
	urlPassRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@?#]*:)([^\s/?#]+)(@)`)
	// urlTokenUserRe matches a user-only userinfo long enough to be a
	// token (https://ghp_xxx@github.com); short names such as git@ stay.
	urlTokenUserRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/\s:@?#\[\]]{20,})(@)`)
	// authHeaderRe matches Authorization / Proxy-Authorization header values.
	authHeaderRe = regexp.MustCompile(`(?i)((?:proxy-)?authorization["']?\s*[:=]\s*["']?)((?:basic|bearer|digest|token|negotiate|apikey)\s+)?([^\s"',;]+)`)
	// cookieRe matches Cookie / Set-Cookie header values to the end of the line.
	cookieRe = regexp.MustCompile(`(?i)\b((?:set-)?cookie["']?\s*:\s*)([^\r\n"']+)`)
	// bearerRe matches a bare bearer token.
	bearerRe = regexp.MustCompile(`(?i)\b(bearer\s+)([A-Za-z0-9._~+/=-]+)`)
	// kvTextRe matches key=value and key: value pairs with a secret-looking key.
	kvTextRe = regexp.MustCompile(`(?i)([\w.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|requirepass|masterauth)[\w.-]*)(["']?\s*[=:]\s*["']?)([^\s"',;&]+)`)
	// exemptFlagRe matches secret-looking names whose value is not a
	// secret: booleans (--no-auth, --password-stdin) and references
	// (--password-file, --auth-source, TOKEN_URL).
	exemptFlagRe = regexp.MustCompile(`(?i)(^(no|skip|disable|without)[-_]|^ask|[-_.]?(file|path|dir|stdin|prompt|env|type|mode|method|mechanism|source|database|db|url|uri|host|port|user|username|timeout|enabled|required|length|policy|ttl)$)`)
	varRefRe     = regexp.MustCompile(`^\$(\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z_][A-Za-z0-9_]*)$`)
)

// RedactText removes credentials from free text (log lines, probe output,
// error messages, single arguments): URL userinfo passwords and token-like
// users, Authorization and Cookie header values, bearer tokens, and
// key=value or key: value pairs with a secret-looking key.
func RedactText(s string) string {
	if s == "" {
		return s
	}
	s = urlPassRe.ReplaceAllString(s, "${1}"+redacted+"${3}")
	s = urlTokenUserRe.ReplaceAllString(s, "${1}"+redacted+"${3}")
	s = authHeaderRe.ReplaceAllString(s, "${1}${2}"+redacted)
	s = cookieRe.ReplaceAllString(s, "${1}"+redacted)
	s = bearerRe.ReplaceAllString(s, "${1}"+redacted)
	return replaceKV(s)
}

// replaceKV applies kvTextRe, keeping exempt keys, variable references and
// values that are already redacted.
func replaceKV(s string) string {
	idx := kvTextRe.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		key, val := s[m[2]:m[3]], s[m[6]:m[7]]
		if exemptFlagRe.MatchString(strings.TrimLeft(key, "-")) || isVarRef(val) || strings.HasPrefix(val, redacted) {
			continue
		}
		b.WriteString(s[last:m[6]])
		b.WriteString(redacted)
		last = m[7]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// RedactArgs redacts a command's argument vector (exec form) without
// splitting arguments that contain whitespace; see RedactShellArgs.
//
//	--password=xyz, PASSWORD=xyz   -> --password=[REDACTED], PASSWORD=[REDACTED]
//	--token xyz                    -> --token [REDACTED]
//	redis-cli -a xyz               -> redis-cli -a [REDACTED]
//	mysql -pxyz                    -> mysql -p[REDACTED]
//	curl -u bob:xyz                -> curl -u bob:[REDACTED]
//	postgres://user:xyz@host       -> postgres://user:[REDACTED]@host
func RedactArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := slices.Clone(args)
	redactTokens(out, 0)
	return out
}

// maxShellDepth bounds nested shell strings (sh -c "su -c '...'").
const maxShellDepth = 3

// RedactShellArgs is RedactArgs plus shell awareness: an argument that
// contains whitespace (the script in ["sh","-c","redis-cli -a x ping"], a
// CMD-SHELL healthcheck) is tokenized with POSIX quoting rules and redacted
// word by word, with program context reset at ;, &&, || and |. Unchanged
// arguments keep their exact text; a changed word is replaced in place.
func RedactShellArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := slices.Clone(args)
	redactTokens(out, maxShellDepth)
	return out
}

type valueKind uint8

const (
	valNone     valueKind = iota
	valSecret             // the whole value is a credential
	valUserPass           // user:password; only the part after the first ':' is secret
)

// program describes a well-known client's credential options.
type program struct {
	short    map[byte]valueKind   // -X VALUE / -XVALUE
	long     map[string]valueKind // --name VALUE / --name=VALUE
	clusters bool                 // short options combine (-su VALUE)
	stopAt   bool                 // options end at the first positional (sshpass -p x COMMAND)
	login    bool                 // rules apply only after a "login" subcommand
}

var programs = func() map[string]*program {
	m := map[string]*program{}
	add := func(p *program, names ...string) {
		for _, n := range names {
			m[n] = p
		}
	}
	add(&program{short: map[byte]valueKind{'a': valSecret}},
		"redis-cli", "valkey-cli", "keydb-cli", "redis-benchmark", "valkey-benchmark")
	add(&program{short: map[byte]valueKind{'p': valSecret}},
		"mysql", "mysqladmin", "mysqldump", "mysqlimport", "mysqlshow", "mysqlcheck", "mysqlpump", "mysqlslap", "mysqlsh",
		"mariadb", "mariadb-admin", "mariadb-dump", "mariadb-import", "mariadb-show", "mariadb-check", "mariadb-slap",
		"mongosh", "mongo", "mongodump", "mongorestore", "mongoexport", "mongoimport", "mongostat", "mongotop", "mongofiles")
	// PostgreSQL tools take credentials only via URL, PGPASSWORD or a
	// password= conninfo word (generic rules); -p is the port.
	add(&program{},
		"psql", "pg_dump", "pg_dumpall", "pg_restore", "pg_isready", "pg_basebackup", "pgbench",
		"createdb", "dropdb", "createuser", "dropuser", "vacuumdb", "reindexdb", "clusterdb")
	add(&program{
		short:    map[byte]valueKind{'u': valUserPass, 'U': valUserPass, 'E': valUserPass, 'b': valSecret},
		long:     map[string]valueKind{"user": valUserPass, "proxy-user": valUserPass, "cert": valUserPass, "proxy-cert": valUserPass, "oauth2-bearer": valSecret},
		clusters: true,
	}, "curl")
	// wget: -p is --page-requisites; --password, --http-password,
	// --ftp-password and --proxy-password are generic.
	add(&program{}, "wget")
	add(&program{short: map[byte]valueKind{'p': valSecret}, stopAt: true}, "sshpass")
	add(&program{short: map[byte]valueKind{'p': valSecret}, login: true}, "docker", "podman", "nerdctl")
	return m
}()

// redactTokens redacts argument words in place. depth > 0 lets words that
// contain whitespace be parsed as shell strings.
func redactTokens(toks []string, depth int) {
	var prog *program
	login := false
	pending := valNone
	for i, t := range toks {
		if pending != valNone {
			k := pending
			pending = valNone
			if !looksLikeOption(t) && !isShellOp(t) {
				toks[i] = redactValue(t, k)
				continue
			}
		}
		if isShellOp(t) {
			prog, login = nil, false
			continue
		}
		if len(t) < 2 || t[0] != '-' || t == "--" {
			if p, ok := programs[progName(t)]; ok {
				prog, login = p, false
				continue
			}
			if prog != nil && prog.stopAt {
				prog = nil
			} else if prog != nil && prog.login && t == "login" {
				login = true
			}
			toks[i] = redactWord(t, depth)
			continue
		}
		active := prog != nil && (!prog.login || login)
		if strings.HasPrefix(t, "--") {
			name, val, hasEq := strings.Cut(t[2:], "=")
			k := valNone
			if active {
				k = prog.long[name]
			}
			if k == valNone && isSecretFlag(name) {
				k = valSecret
			}
			if k != valNone {
				if hasEq {
					toks[i] = "--" + name + "=" + redactValue(val, k)
				} else {
					pending = k
				}
				continue
			}
			toks[i] = redactWord(t, depth)
			continue
		}
		body := t[1:]
		if active {
			if k := prog.short[body[0]]; k != valNone {
				if len(body) == 1 {
					pending = k
				} else {
					toks[i] = t[:2] + redactValue(body[1:], k)
				}
				continue
			}
			if prog.clusters && isLetters(body) {
				if k := prog.short[body[len(body)-1]]; k != valNone {
					pending = k
					continue
				}
			}
		}
		if len(body) > 2 && !strings.ContainsRune(body, '=') && isSecretFlag(body) {
			pending = valSecret
			continue
		}
		toks[i] = redactWord(t, depth)
	}
}

// redactWord redacts a word that is not an option value: a shell string
// (when depth allows), a KEY=VALUE assignment, or free text.
func redactWord(t string, depth int) string {
	if depth > 0 && strings.ContainsAny(t, " \t\n") {
		return redactShell(t, depth-1)
	}
	if out, ok := redactAssignment(t); ok {
		return out
	}
	return RedactText(t)
}

// redactAssignment redacts the value of KEY=VALUE, --opt=KEY=VALUE and
// -DKEY=VALUE words whose (innermost) key looks secret.
func redactAssignment(t string) (string, bool) {
	prev := 0
	for {
		eq := strings.IndexByte(t[prev:], '=')
		if eq < 0 {
			return "", false
		}
		eq += prev
		key := t[prev:eq]
		if prev == 0 {
			key = strings.TrimLeft(key, "-")
		}
		if key == "" || strings.ContainsAny(key, " \t\n\"'") {
			return "", false
		}
		if isSecretFlag(key) {
			if eq+1 == len(t) {
				return t, true
			}
			return t[:eq+1] + redactValue(t[eq+1:], valSecret), true
		}
		prev = eq + 1
	}
}

func redactValue(v string, k valueKind) string {
	if v == "" || isVarRef(v) {
		return v
	}
	if k == valUserPass {
		user, pass, ok := strings.Cut(v, ":")
		if !ok || pass == "" || isVarRef(pass) {
			return v
		}
		return user + ":" + redacted
	}
	return redacted
}

func isSecretFlag(name string) bool {
	return IsSecretKey(name) && !exemptFlagRe.MatchString(name)
}

func isVarRef(v string) bool { return varRefRe.MatchString(v) }

// looksLikeOption reports whether t is an option rather than an option's
// value: a long option, or a single-letter short option such as -h.
func looksLikeOption(t string) bool {
	if strings.HasPrefix(t, "--") {
		return len(t) > 2
	}
	return len(t) == 2 && t[0] == '-' && isLetters(t[1:])
}

func isLetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func isShellOp(t string) bool {
	switch t {
	case ";", "&&", "||", "|", "&", "\n":
		return true
	}
	return false
}

func progName(t string) string {
	t = t[strings.LastIndexByte(t, '/')+1:]
	return strings.TrimSuffix(strings.ToLower(t), ".exe")
}

// shellTok is one word or control operator of a shell string, with its byte
// span in the source and its unquoted value.
type shellTok struct {
	start, end int
	val        string
}

// shellTokens splits s into words and the control operators ; & && | || and
// newline, honouring single quotes, double quotes and backslash escapes. It
// is a display-grade tokenizer: no expansion is performed.
func shellTokens(s string) []shellTok {
	var out []shellTok
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case ' ', '\t', '\r':
			i++
			continue
		case '\n', ';':
			out = append(out, shellTok{i, i + 1, s[i : i+1]})
			i++
			continue
		case '&', '|':
			j := i + 1
			if j < len(s) && s[j] == c {
				j++
			}
			out = append(out, shellTok{i, j, s[i:j]})
			i = j
			continue
		}
		start := i
		var b strings.Builder
	word:
		for i < len(s) {
			c := s[i]
			switch c {
			case ' ', '\t', '\r', '\n', ';', '|':
				break word
			case '&':
				// 2>&1 and <&0 are redirections, not operators.
				if i == start || (s[i-1] != '>' && s[i-1] != '<') {
					break word
				}
				b.WriteByte(c)
				i++
			case '\\':
				if i+1 < len(s) {
					b.WriteByte(s[i+1])
				}
				i += 2
			case '\'':
				j := strings.IndexByte(s[i+1:], '\'')
				if j < 0 {
					b.WriteString(s[i+1:])
					i = len(s)
				} else {
					b.WriteString(s[i+1 : i+1+j])
					i += j + 2
				}
			case '"':
				i++
				for i < len(s) && s[i] != '"' {
					if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
						b.WriteByte(s[i+1])
						i += 2
						continue
					}
					b.WriteByte(s[i])
					i++
				}
				i++
			default:
				b.WriteByte(c)
				i++
			}
		}
		if i > len(s) {
			i = len(s)
		}
		out = append(out, shellTok{start, i, b.String()})
	}
	return out
}

// redactShell redacts a shell string: free-text rules over the whole
// string (headers that span words), then word-level rules. Only changed
// words are rewritten, re-quoted when needed.
func redactShell(s string, depth int) string {
	s = RedactText(s)
	toks := shellTokens(s)
	if len(toks) == 0 {
		return s
	}
	vals := make([]string, len(toks))
	for i, t := range toks {
		vals[i] = t.val
	}
	redactTokens(vals, depth)
	var b strings.Builder
	last := 0
	for i, t := range toks {
		if vals[i] == t.val {
			continue
		}
		b.WriteString(s[last:t.start])
		b.WriteString(requote(vals[i]))
		last = t.end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func requote(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\r\n'\"\\$`;&|<>()") {
		return ShellQuote(v)
	}
	return v
}
