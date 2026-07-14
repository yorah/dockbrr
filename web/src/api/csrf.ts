export function readCsrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)dockbrr_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}
