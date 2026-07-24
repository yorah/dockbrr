import { expect, test } from "vitest";
import { jobQueryOptions } from "./queries";

test("jobQueryOptions builds the per-job query key", () => {
  expect(jobQueryOptions(5).queryKey).toEqual(["job", 5]);
});

test("jobQueryOptions stops polling on a terminal status and polls otherwise", () => {
  const opts = jobQueryOptions(5);
  const at = (status: string | undefined) =>
    opts.refetchInterval({ state: { data: status ? { status } : undefined } } as never);
  expect(at("running")).toBe(1500);
  expect(at("queued")).toBe(1500);
  expect(at(undefined)).toBe(1500);
  expect(at("success")).toBe(false);
  expect(at("failed")).toBe(false);
  expect(at("canceled")).toBe(false);
});
