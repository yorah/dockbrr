import { QueryClient } from "@tanstack/react-query";

export function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 5_000, refetchOnWindowFocus: false },
      mutations: { retry: false },
    },
  });
}
