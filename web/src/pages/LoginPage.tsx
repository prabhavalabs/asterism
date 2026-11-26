import { useId, useState, type FormEvent } from "react";
import { Alert02Icon, Loading03Icon, Moon02Icon, SecurityCheckIcon, Sun03Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { BrandMark } from "@/components/layout/BrandMark";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { loginAdmin, type AuthSession } from "@/lib/rpc";
import { useTheme } from "@/lib/theme";

interface LoginPageProps {
  onAuthenticated: (session: AuthSession) => void;
}

export function LoginPage({ onAuthenticated }: LoginPageProps) {
  const { resolvedTheme, toggle } = useTheme();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const emailId = useId();
  const passwordId = useId();
  const errorId = useId();

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const session = await loginAdmin(email, password);
      onAuthenticated(session);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Unable to sign in");
    } finally {
      setSubmitting(false);
    }
  };

  const nextTheme = resolvedTheme === "dark" ? "light" : "dark";

  return (
    <main className="relative flex min-h-svh flex-col items-center justify-center bg-background px-4 py-12 text-foreground">
      <div
        aria-hidden="true"
        className="pointer-events-none fixed inset-0 bg-[radial-gradient(var(--iso-grid-dot)_1px,transparent_1px)] bg-size-[20px_20px]"
      />

      <Button
        variant="ghost"
        size="icon"
        className="absolute top-4 right-4 z-10"
        onClick={toggle}
        aria-label={`Switch to ${nextTheme} theme`}
        title={`Switch to ${nextTheme} theme`}
      >
        {resolvedTheme === "dark" ? <Icon icon={Sun03Icon} /> : <Icon icon={Moon02Icon} />}
      </Button>

      <div className="relative z-10 flex w-full max-w-[400px] flex-col gap-5">
        <div className="flex items-center gap-3 self-center">
          <BrandMark className="size-9" />
          <div className="flex flex-col">
            <span className="font-heading text-heading text-foreground">Asterism</span>
            <span className="text-small text-muted-foreground">VPS observatory</span>
          </div>
        </div>

        <Card className="shadow-pop">
          <form onSubmit={handleSubmit} className="flex flex-col gap-5 p-5">
            <div className="flex flex-col gap-1">
              <h1 className="font-heading text-title text-foreground">Sign in</h1>
              <p className="text-control text-muted-foreground">Use the administrator account from asterism.toml.</p>
            </div>

            <div className="flex flex-col gap-4">
              <div className="flex flex-col gap-1.5">
                <label htmlFor={emailId} className="text-small font-medium text-text-2">
                  Email
                </label>
                <Input
                  id={emailId}
                  type="email"
                  autoComplete="username"
                  placeholder="admin@example.com"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  aria-invalid={error ? true : undefined}
                  aria-describedby={error ? errorId : undefined}
                  required
                />
              </div>

              <div className="flex flex-col gap-1.5">
                <label htmlFor={passwordId} className="text-small font-medium text-text-2">
                  Password
                </label>
                <Input
                  id={passwordId}
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  aria-invalid={error ? true : undefined}
                  aria-describedby={error ? errorId : undefined}
                  required
                />
              </div>
            </div>

            {error && (
              <div
                id={errorId}
                role="alert"
                className="flex items-start gap-2 rounded-tile bg-status-critical-surface px-3 py-2.5 text-small text-status-critical"
              >
                <Icon icon={Alert02Icon} className="mt-px size-4 shrink-0" />
                <span>{error}</span>
              </div>
            )}

            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting && <Icon icon={Loading03Icon} className="animate-spin motion-reduce:animate-none" />}
              {submitting ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </Card>

        <p className="flex items-center justify-center gap-2 text-small text-muted-foreground">
          <Icon icon={SecurityCheckIcon} className="size-4 shrink-0 text-status-healthy" />
          Argon2id password · HttpOnly, SameSite=Strict session
        </p>
      </div>
    </main>
  );
}
