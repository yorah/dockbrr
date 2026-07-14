import type { ReactNode } from "react";
import { useMe, useSetupStatus } from "@/hooks/queries";
import { LoginScreen } from "@/routes/login";
import { SetupScreen } from "@/routes/setup";

export function AuthGate({ children }: { children: ReactNode }) {
  const setup = useSetupStatus();
  const me = useMe();

  if (setup.isLoading) return <div className="p-8 text-center text-sm opacity-70">Loading…</div>;
  // A failed /setup/status probe (e.g. a 500) must surface as an error, not
  // silently fall through to the login screen (which would misrepresent the
  // server state and, on first run, hide the setup flow).
  if (setup.isError) {
    return (
      <div role="alert" className="p-8 text-center text-sm text-danger">
        Couldn't reach the server. Please refresh and try again.
      </div>
    );
  }
  if (setup.data?.needs_setup) return <SetupScreen />;
  if (me.isLoading) return <div className="p-8 text-center text-sm opacity-70">Loading…</div>;
  if (me.isError) return <LoginScreen />;
  return <>{children}</>;
}
