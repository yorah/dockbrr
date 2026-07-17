import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeSanitize from "rehype-sanitize";
import type { ComponentPropsWithoutRef } from "react";

// Renders untrusted changelog markdown to React elements, no `dangerouslySetInnerHTML`
// anywhere. Sanitize runs on the HAST via rehype-sanitize (default GitHub-ish allowlist
// schema): it drops <script>, on* event-handler attributes, and non-http(s)/mailto URL
// schemes (e.g. `javascript:`). This is defense-in-depth over the Phase-4 server-side
// text sanitize, so treat the markdown as hostile input regardless of what the API sends.
export function Changelog({ markdown, status }: { markdown: string; status?: string }) {
  if (!markdown) {
    if (status === "rate_limited") {
      return (
        <p className="text-sm opacity-60">
          GitHub rate limit reached. Changelog unavailable until the limit resets.{" "}
          <a href="/settings/registries" className="text-primary hover:underline">
            Add a GitHub token in Settings
          </a>{" "}
          to raise the limit.
        </p>
      );
    }
    return <p className="text-sm opacity-60">No changelog available.</p>;
  }
  return (
    <div className="changelog-body max-w-none text-sm">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSanitize]}
        components={{
          a: ({ href, children, ...rest }: ComponentPropsWithoutRef<"a">) => (
            <a href={href} target="_blank" rel="noopener noreferrer" {...rest}>
              {children}
            </a>
          ),
        }}
      >
        {markdown}
      </ReactMarkdown>
    </div>
  );
}
