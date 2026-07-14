import { createRoute, createRouter, redirect } from "@tanstack/react-router";
import { rootRoute } from "@/routes/__root";
import { DashboardScreen } from "@/routes/dashboard";
import { ServiceScreen } from "@/routes/service.$id";
import { ProjectScreen } from "@/routes/project.$id";
import { SettingsScreen } from "@/routes/settings";
import { JobsScreen } from "@/routes/jobs";
import { ApplicationSettings } from "@/components/settings/ApplicationSettings";
import { ScanningSettings } from "@/components/settings/ScanningSettings";
import { UpdatesSettings } from "@/components/settings/UpdatesSettings";
import { AutoUpdateToggles } from "@/components/settings/AutoUpdateToggles";
import { RegistriesSettings } from "@/components/settings/RegistriesSettings";
import { PasswordSettings } from "@/components/settings/PasswordSettings";
import { LogsSettings } from "@/components/settings/LogsSettings";

const dashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: DashboardScreen,
});

const serviceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/service/$id",
  component: ServiceScreen,
});

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/project/$id",
  component: ProjectScreen,
});

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: SettingsScreen,
});

// /settings itself has no content, so land on Application.
const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings/application" });
  },
});

// Written out individually (not via .map()) so each route's `path` stays a
// string literal. A mapped array widens to `Route[]` and addChildren can no
// longer compute per-child full paths, which silently drops them from the
// typed route tree (and from redirect()'s `to` union below).
const applicationRoute = createRoute({ getParentRoute: () => settingsRoute, path: "application", component: ApplicationSettings });
const scanningRoute = createRoute({ getParentRoute: () => settingsRoute, path: "scanning", component: ScanningSettings });
const updatesRoute = createRoute({ getParentRoute: () => settingsRoute, path: "updates", component: UpdatesSettings });
const autoUpdateRoute = createRoute({ getParentRoute: () => settingsRoute, path: "auto-update", component: AutoUpdateToggles });
const registriesRoute = createRoute({ getParentRoute: () => settingsRoute, path: "registries", component: RegistriesSettings });
const securityRoute = createRoute({ getParentRoute: () => settingsRoute, path: "security", component: PasswordSettings });
const logsRoute = createRoute({ getParentRoute: () => settingsRoute, path: "logs", component: LogsSettings });

const settingsRouteWithChildren = settingsRoute.addChildren([
  settingsIndexRoute,
  applicationRoute,
  scanningRoute,
  updatesRoute,
  autoUpdateRoute,
  registriesRoute,
  securityRoute,
  logsRoute,
]);

const jobsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/jobs",
  component: JobsScreen,
});

// Exported so tests can build a fresh router (with memory history) per test:
// the `router` singleton below carries navigation state across test cases.
export const routeTree = rootRoute.addChildren([
  dashboardRoute,
  serviceRoute,
  projectRoute,
  settingsRouteWithChildren,
  jobsRoute,
]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
