import { createRootRoute } from "@tanstack/react-router";
import { AuthGate } from "@/auth/AuthGate";
import { AppLayout } from "@/components/AppLayout";

export const rootRoute = createRootRoute({
  component: () => (
    <AuthGate>
      <AppLayout />
    </AuthGate>
  ),
});
