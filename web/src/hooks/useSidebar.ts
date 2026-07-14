import { useCallback, useEffect, useState } from "react";

export const SIDEBAR_STORAGE_KEY = "dockbrr:sidebar";

const NARROW = "(max-width: 767px)";

function initial(): boolean {
  if (typeof window === "undefined") return false;
  if (window.matchMedia?.(NARROW).matches) return true;
  return window.localStorage.getItem(SIDEBAR_STORAGE_KEY) === "collapsed";
}

function initialNarrow(): boolean {
  if (typeof window === "undefined") return false;
  return window.matchMedia?.(NARROW).matches ?? false;
}

/**
 * Collapsed = icon rail. Persisted to localStorage so it survives reloads,
 * like the theme preference. A viewport that becomes narrow forces the rail;
 * widening again leaves the user's choice alone. `isNarrow` tracks the same
 * media query so callers can render the expanded sidebar as an overlay
 * instead of squeezing the content column on small screens.
 */
export function useSidebar(): { collapsed: boolean; toggle: () => void; isNarrow: boolean } {
  const [collapsed, setCollapsed] = useState<boolean>(initial);
  const [isNarrow, setIsNarrow] = useState<boolean>(initialNarrow);

  useEffect(() => {
    const mql = window.matchMedia?.(NARROW);
    if (!mql) return;
    const onChange = (e: { matches: boolean }) => {
      setIsNarrow(e.matches);
      if (e.matches) setCollapsed(true);
    };
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  const toggle = useCallback(() => {
    setCollapsed((prev) => {
      const next = !prev;
      window.localStorage.setItem(SIDEBAR_STORAGE_KEY, next ? "collapsed" : "expanded");
      return next;
    });
  }, []);

  return { collapsed, toggle, isNarrow };
}
