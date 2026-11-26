package dockercli

import (
	"slices"
	"strings"
	"testing"
)

const pw = "s3cr3t-value"

// TestRedactArgsRules is the golden table for exec-form argument vectors.
func TestRedactArgsRules(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		// Program-aware short flags.
		{"redis-cli -a", []string{"redis-cli", "-h", "cache", "-p", "6379", "-a", pw, "ping"}, []string{"redis-cli", "-h", "cache", "-p", "6379", "-a", "[REDACTED]", "ping"}},
		{"valkey-cli by path", []string{"/usr/local/bin/valkey-cli", "-a", pw}, []string{"/usr/local/bin/valkey-cli", "-a", "[REDACTED]"}},
		{"redis-cli --pass", []string{"redis-cli", "--pass", pw}, []string{"redis-cli", "--pass", "[REDACTED]"}},
		{"redis-cli -u url", []string{"redis-cli", "-u", "redis://:" + pw + "@cache:6379/0"}, []string{"redis-cli", "-u", "redis://:[REDACTED]@cache:6379/0"}},
		{"mysql -pPW", []string{"mysql", "-uroot", "-p" + pw, "-h", "db", "-P", "3306"}, []string{"mysql", "-uroot", "-p[REDACTED]", "-h", "db", "-P", "3306"}},
		{"mysql -p PW", []string{"mysql", "-u", "root", "-p", pw}, []string{"mysql", "-u", "root", "-p", "[REDACTED]"}},
		{"mysql -p prompt", []string{"mysql", "-u", "root", "-p", "-h", "db"}, []string{"mysql", "-u", "root", "-p", "-h", "db"}},
		{"mysqladmin --password=", []string{"mysqladmin", "--password=" + pw, "ping"}, []string{"mysqladmin", "--password=[REDACTED]", "ping"}},
		{"mariadb-dump -p", []string{"mariadb-dump", "-p" + pw, "shop"}, []string{"mariadb-dump", "-p[REDACTED]", "shop"}},
		{"mongosh -p", []string{"mongosh", "-u", "admin", "-p", pw, "--authenticationDatabase", "admin"}, []string{"mongosh", "-u", "admin", "-p", "[REDACTED]", "--authenticationDatabase", "admin"}},
		{"mongodump url", []string{"mongodump", "--uri=mongodb://admin:" + pw + "@mongo:27017"}, []string{"mongodump", "--uri=mongodb://admin:[REDACTED]@mongo:27017"}},
		{"psql -p is a port", []string{"psql", "-h", "db", "-p", "5432", "-U", "app"}, []string{"psql", "-h", "db", "-p", "5432", "-U", "app"}},
		{"psql url", []string{"psql", "postgres://app:" + pw + "@db:5432/app"}, []string{"psql", "postgres://app:[REDACTED]@db:5432/app"}},
		{"pg_isready -d url", []string{"pg_isready", "-d", "postgresql://app:" + pw + "@db/app", "-p", "5432"}, []string{"pg_isready", "-d", "postgresql://app:[REDACTED]@db/app", "-p", "5432"}},
		{"psql conninfo", []string{"psql", "host=db user=app password=" + pw}, []string{"psql", "host=db user=app password=[REDACTED]"}},
		{"PGPASSWORD env", []string{"env", "PGPASSWORD=" + pw, "psql", "-h", "db"}, []string{"env", "PGPASSWORD=[REDACTED]", "psql", "-h", "db"}},
		{"curl -u", []string{"curl", "-fsS", "-u", "admin:" + pw, "http://localhost/health"}, []string{"curl", "-fsS", "-u", "admin:[REDACTED]", "http://localhost/health"}},
		{"curl -uUSER:PW", []string{"curl", "-uadmin:" + pw, "http://x"}, []string{"curl", "-uadmin:[REDACTED]", "http://x"}},
		{"curl --user=", []string{"curl", "--user=admin:" + pw, "http://x"}, []string{"curl", "--user=admin:[REDACTED]", "http://x"}},
		{"curl --user", []string{"curl", "--user", "admin:" + pw}, []string{"curl", "--user", "admin:[REDACTED]"}},
		{"curl cluster -su", []string{"curl", "-su", "admin:" + pw, "http://x"}, []string{"curl", "-su", "admin:[REDACTED]", "http://x"}},
		{"curl -U proxy", []string{"curl", "-U", "proxy:" + pw, "http://x"}, []string{"curl", "-U", "proxy:[REDACTED]", "http://x"}},
		{"curl user only", []string{"curl", "-u", "admin", "http://x"}, []string{"curl", "-u", "admin", "http://x"}},
		{"curl auth header", []string{"curl", "-H", "Authorization: Basic YWRtaW46czNjcjN0", "http://x"}, []string{"curl", "-H", "Authorization: Basic [REDACTED]", "http://x"}},
		{"curl bearer header", []string{"curl", "-H", "Authorization: Bearer " + pw}, []string{"curl", "-H", "Authorization: Bearer [REDACTED]"}},
		{"curl cookie header", []string{"curl", "-H", "Cookie: sid=" + pw}, []string{"curl", "-H", "Cookie: [REDACTED]"}},
		{"curl -b cookie", []string{"curl", "-b", "sid=" + pw, "--cookie", "sid=" + pw}, []string{"curl", "-b", "[REDACTED]", "--cookie", "[REDACTED]"}},
		{"wget --password", []string{"wget", "--user=bob", "--password=" + pw, "-p", "http://x"}, []string{"wget", "--user=bob", "--password=[REDACTED]", "-p", "http://x"}},
		{"wget --http-password", []string{"wget", "--http-password", pw}, []string{"wget", "--http-password", "[REDACTED]"}},
		{"sshpass -p", []string{"sshpass", "-p", pw, "ssh", "-p", "2222", "deploy@host"}, []string{"sshpass", "-p", "[REDACTED]", "ssh", "-p", "2222", "deploy@host"}},
		{"sshpass -pPW", []string{"sshpass", "-p" + pw, "rsync", "-a", "x", "y"}, []string{"sshpass", "-p[REDACTED]", "rsync", "-a", "x", "y"}},
		{"docker login -p", []string{"docker", "login", "-u", "bob", "-p", pw, "ghcr.io"}, []string{"docker", "login", "-u", "bob", "-p", "[REDACTED]", "ghcr.io"}},
		{"docker login --password-stdin", []string{"docker", "login", "--password-stdin", "-u", "bob"}, []string{"docker", "login", "--password-stdin", "-u", "bob"}},
		// Generic long flags and KEY=VALUE.
		{"--password", []string{"app", "--password", pw}, []string{"app", "--password", "[REDACTED]"}},
		{"--pass=", []string{"app", "--pass=" + pw}, []string{"app", "--pass=[REDACTED]"}},
		{"--db-pass", []string{"app", "--db-pass", pw}, []string{"app", "--db-pass", "[REDACTED]"}},
		{"-password go flag", []string{"app", "-password", pw}, []string{"app", "-password", "[REDACTED]"}},
		{"-Dprop=", []string{"java", "-Djavax.net.ssl.keyStorePassword=" + pw, "-jar", "app.jar"}, []string{"java", "-Djavax.net.ssl.keyStorePassword=[REDACTED]", "-jar", "app.jar"}},
		{"-e KEY=VALUE", []string{"docker", "run", "-e", "DB_PASSWORD=" + pw, "-e", "PORT=8080", "-p", "8080:80", "app"}, []string{"docker", "run", "-e", "DB_PASSWORD=[REDACTED]", "-e", "PORT=8080", "-p", "8080:80", "app"}},
		{"--env=KEY=VALUE", []string{"docker", "run", "--env=API_TOKEN=" + pw, "--env", "MODE=prod"}, []string{"docker", "run", "--env=API_TOKEN=[REDACTED]", "--env", "MODE=prod"}},
		{"-eKEY=VALUE", []string{"podman", "run", "-eSECRET_KEY_BASE=" + pw}, []string{"podman", "run", "-eSECRET_KEY_BASE=[REDACTED]"}},
		{"--build-arg", []string{"docker", "build", "--build-arg", "NPM_TOKEN=" + pw, "."}, []string{"docker", "build", "--build-arg", "NPM_TOKEN=[REDACTED]", "."}},
		{"helm --set", []string{"helm", "upgrade", "--set", "db.password=" + pw}, []string{"helm", "upgrade", "--set", "db.password=[REDACTED]"}},
		{"url query token", []string{"app", "--url=https://h/x?token=" + pw}, []string{"app", "--url=https://h/x?token=[REDACTED]"}},
		{"url anywhere", []string{"app", "--upstream", "amqp://guest:" + pw + "@rabbit:5672/"}, []string{"app", "--upstream", "amqp://guest:[REDACTED]@rabbit:5672/"}},
		{"url password with @", []string{"postgres://u:p@ss" + pw + "@db/app"}, []string{"postgres://u:[REDACTED]@db/app"}},
		{"url token user", []string{"git", "clone", "https://ghp_" + strings.Repeat("a", 36) + "@github.com/o/r"}, []string{"git", "clone", "https://[REDACTED]@github.com/o/r"}},
		{"variable reference kept", []string{"redis-cli", "-a", "$REDIS_PASSWORD", "--password=${DB_PASSWORD}"}, []string{"redis-cli", "-a", "$REDIS_PASSWORD", "--password=${DB_PASSWORD}"}},
		{"variable default redacted", []string{"app", "--password=${PW:-" + pw + "}"}, []string{"app", "--password=[REDACTED]"}},
		// Must stay untouched.
		{"docker run -p", []string{"docker", "run", "-p", "8080:80", "-p", "127.0.0.1:5432:5432", "nginx"}, []string{"docker", "run", "-p", "8080:80", "-p", "127.0.0.1:5432:5432", "nginx"}},
		{"ps -p", []string{"ps", "-p", "1", "-o", "rss="}, []string{"ps", "-p", "1", "-o", "rss="}},
		{"tail -n", []string{"tail", "-n", "100", "-f", "/var/log/app.log"}, []string{"tail", "-n", "100", "-f", "/var/log/app.log"}},
		{"ssh -p", []string{"ssh", "-p", "22", "git@github.com"}, []string{"ssh", "-p", "22", "git@github.com"}},
		{"nginx -g", []string{"nginx", "-g", "daemon off;"}, []string{"nginx", "-g", "daemon off;"}},
		{"redis-server -p-less", []string{"redis-server", "--port", "6379", "--appendonly", "yes"}, []string{"redis-server", "--port", "6379", "--appendonly", "yes"}},
		{"--password-file", []string{"app", "--password-file", "/run/secrets/db", "POSTGRES_PASSWORD_FILE=/run/secrets/pg"}, []string{"app", "--password-file", "/run/secrets/db", "POSTGRES_PASSWORD_FILE=/run/secrets/pg"}},
		{"--no-auth then option", []string{"app", "--no-auth", "--listen", ":80"}, []string{"app", "--no-auth", "--listen", ":80"}},
		{"secret flag then short option", []string{"app", "--auth", "-v"}, []string{"app", "--auth", "-v"}},
		{"auth source kept", []string{"mongosh", "--authSource", "admin", "--authenticationMechanism", "SCRAM-SHA-256"}, []string{"mongosh", "--authSource", "admin", "--authenticationMechanism", "SCRAM-SHA-256"}},
		{"author", []string{"--author", "me", "AUTHOR=me"}, []string{"--author", "me", "AUTHOR=me"}},
		{"ssh user url", []string{"ssh://git@github.com/o/r", "http://host:8080/x"}, []string{"ssh://git@github.com/o/r", "http://host:8080/x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactArgs(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("RedactArgs(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			if again := RedactArgs(got); !slices.Equal(again, got) {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
			if sh := RedactShellArgs(tc.in); !slices.Equal(sh, got) {
				t.Fatalf("RedactShellArgs differs on exec form: %q vs %q", sh, got)
			}
		})
	}
}

// TestRedactShellArgsRules is the golden table for shell strings
// (["sh","-c", script] and CMD-SHELL healthchecks).
func TestRedactShellArgsRules(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"redis-cli -a", "redis-cli -a " + pw + " ping", "redis-cli -a [REDACTED] ping"},
		{"exec wrapper", "exec /usr/bin/redis-cli -h cache -a '" + pw + "' --no-auth-warning ping", "exec /usr/bin/redis-cli -h cache -a [REDACTED] --no-auth-warning ping"},
		{"mysqladmin -p", "mysqladmin ping -h localhost -uroot -p" + pw + " --silent", "mysqladmin ping -h localhost -uroot -p[REDACTED] --silent"},
		{"mysql -p quoted", `mysql -uroot -p"` + pw + `" -e 'select 1'`, "mysql -uroot -p[REDACTED] -e 'select 1'"},
		{"mongosh", "mongosh --quiet -u root -p " + pw + " --eval 'db.adminCommand(\"ping\")'", "mongosh --quiet -u root -p [REDACTED] --eval 'db.adminCommand(\"ping\")'"},
		{"pg_isready", "pg_isready -U app -d app -h 127.0.0.1 -p 5432", "pg_isready -U app -d app -h 127.0.0.1 -p 5432"},
		{"psql url", `psql "postgres://app:` + pw + `@db:5432/app" -c 'select 1'`, `psql "postgres://app:[REDACTED]@db:5432/app" -c 'select 1'`},
		{"curl -u", "curl -fsSu admin:" + pw + " http://localhost:8080/health || exit 1", "curl -fsSu admin:[REDACTED] http://localhost:8080/health || exit 1"},
		{"curl header", `curl -H "Authorization: Bearer ` + pw + `" http://x`, `curl -H "Authorization: Bearer [REDACTED]" http://x`},
		{"wget", "wget -qO- --password=" + pw + " --user bob http://x", "wget -qO- --password=[REDACTED] --user bob http://x"},
		{"sshpass then ssh -p", "sshpass -p " + pw + " ssh -p 2222 deploy@host uptime", "sshpass -p [REDACTED] ssh -p 2222 deploy@host uptime"},
		{"chain resets program", "redis-cli -a " + pw + " ping && ps -p 1 && tail -n 5 /log", "redis-cli -a [REDACTED] ping && ps -p 1 && tail -n 5 /log"},
		{"pipe resets program", "mysql -p" + pw + " -e 'show status' | grep -p x", "mysql -p[REDACTED] -e 'show status' | grep -p x"},
		{"redirect is not an operator", "redis-cli -a " + pw + " ping 2>&1 | grep PONG", "redis-cli -a [REDACTED] ping 2>&1 | grep PONG"},
		{"env assignment", "DB_PASSWORD=" + pw + " API_TOKEN='" + pw + "' node server.js", "DB_PASSWORD=[REDACTED] API_TOKEN='[REDACTED]' node server.js"},
		{"docker -e", "docker run --rm -e REDIS_PASSWORD=" + pw + " -p 6379:6379 redis", "docker run --rm -e REDIS_PASSWORD=[REDACTED] -p 6379:6379 redis"},
		{"generic --password", "app --password " + pw + " --mode=fast", "app --password [REDACTED] --mode=fast"},
		{"quoted value with spaces", `app --password "two words" --x`, "app --password [REDACTED] --x"},
		{"nested sh -c", `su app -c "redis-cli -a ` + pw + ` ping"`, `su app -c 'redis-cli -a [REDACTED] ping'`},
		{"variable kept", `redis-cli -a "$REDIS_PASSWORD" ping`, `redis-cli -a "$REDIS_PASSWORD" ping`},
		{"untouched", "nginx -g 'daemon off;'  -p /etc/nginx", "nginx -g 'daemon off;'  -p /etc/nginx"},
		{"untouched docker -p", "docker run -p 8080:80 -e PORT=80 nginx", "docker run -p 8080:80 -e PORT=80 nginx"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactShellArgs([]string{"/bin/sh", "-c", tc.in})
			if got[2] != tc.want {
				t.Fatalf("RedactShellArgs(%q)\n got %q\nwant %q", tc.in, got[2], tc.want)
			}
			if again := RedactShellArgs(got); !slices.Equal(again, got) {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
			if strings.Contains(got[2], pw) {
				t.Fatalf("secret leaked: %q", got[2])
			}
			hc := RedactShellArgs([]string{"CMD-SHELL", tc.in})
			if hc[1] != tc.want {
				t.Fatalf("CMD-SHELL form %q", hc[1])
			}
		})
	}
}

// TestRedactTextRules is the golden table for free text (logs, probe output).
func TestRedactTextRules(t *testing.T) {
	tests := []struct{ in, want string }{
		{"warn: password=" + pw + " rejected", "warn: password=[REDACTED] rejected"},
		{`{"password": "` + pw + `", "user": "bob"}`, `{"password": "[REDACTED]", "user": "bob"}`},
		{"db_password: " + pw, "db_password: [REDACTED]"},
		{"Authorization: Basic YWRtaW46czNjcjN0", "Authorization: Basic [REDACTED]"},
		{"proxy-authorization=" + pw, "proxy-authorization=[REDACTED]"},
		{"got Bearer eyJhbGciOi.x.y from client", "got Bearer [REDACTED] from client"},
		{"Set-Cookie: sid=" + pw + "; Path=/", "Set-Cookie: [REDACTED]"},
		{"connect redis://:" + pw + "@cache:6379 failed", "connect redis://:[REDACTED]@cache:6379 failed"},
		{"dial postgres://app:" + pw + "@db:5432/app", "dial postgres://app:[REDACTED]@db:5432/app"},
		{"password_file=/run/secrets/db", "password_file=/run/secrets/db"},
		{"listening on :8080", "listening on :8080"},
		{"PASS: TestFoo (0.01s)", "PASS: TestFoo (0.01s)"},
		{"see https://example.com/reset-password for help", "see https://example.com/reset-password for help"},
	}
	for _, tc := range tests {
		got := RedactText(tc.in)
		if got != tc.want {
			t.Fatalf("RedactText(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
		if RedactText(got) != got {
			t.Fatalf("not idempotent: %q", got)
		}
	}
}
