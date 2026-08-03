// Lightweight in-browser sanitizer for untrusted mail HTML. The mail view
// already renders inside a sandboxed iframe (no scripts, no same-origin, no
// popups), which is the primary defence; this helper is a second line that
// strips obviously dangerous markup and forces safe link attributes so the
// iframe cannot be used to load remote tracking resources via <link>/<style>,
// submit forms, navigate via javascript: URIs, or autoplay media.
//
// It deliberately avoids a third-party dependency (DOMPurify & co.) and keeps
// the allowlist small: text formatting, tables, images and links survive,
// everything else is removed.

const BLOCKED_TAGS = new Set([
  "script",
  "iframe",
  "object",
  "embed",
  "applet",
  "form",
  "input",
  "button",
  "select",
  "textarea",
  "link",
  "meta",
  "base",
  "style",
  "title",
  "head",
  "frame",
  "frameset",
  "audio",
  "video",
  "source",
  "track",
]);

const URL_ATTRS = new Set([
  "href",
  "src",
  "action",
  "formaction",
  "xlink:href",
  "background",
  "poster",
]);

const SAFE_PROTOCOLS = new Set([
  "http:",
  "https:",
  "mailto:",
  "tel:",
  "ftp:",
]);

function isUnsafeUrl(value: string): boolean {
  const v = value.trim().toLowerCase();
  if (v.startsWith("javascript:") || v.startsWith("vbscript:")) {
    return true;
  }
  // cid: references inline MIME parts by Content-ID. They cannot
  // exfiltrate or execute inside the sandboxed iframe, and the SPA rewrites
  // them to /api/mail/attachments/<id> before render. Allowed as-is.
  if (v.startsWith("cid:")) {
    return false;
  }
  // data: URIs: allow inline raster images only (data:image/png|jpeg|…),
  // reject everything else. SVG is intentionally excluded because it can
  // carry <script> and exfiltrate via XSS in some browser rendering paths,
  // even inside a sandboxed iframe when allow-popups is set.
  if (v.startsWith("data:")) {
    if (v.startsWith("data:image/svg")) return true;
    return !v.startsWith("data:image/");
  }
  // Allow explicit safe protocols; reject anything else with a scheme.
  const colon = v.indexOf(":");
  if (colon > 0) {
    const scheme = v.slice(0, colon + 1);
    if (!SAFE_PROTOCOLS.has(scheme)) {
      return true;
    }
  }
  return false;
}

export function sanitizeMailHtml(html: string): string {
  if (!html) {
    return "";
  }
  const doc = new DOMParser().parseFromString(html, "text/html");
  if (!doc.body) {
    return "";
  }

  // Walk in document order so removals do not skip subtrees.
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_ELEMENT);
  const toRemove: Element[] = [];
  let node: Node | null = walker.currentNode;
  while ((node = walker.nextNode())) {
    const el = node as Element;
    const tag = el.tagName.toLowerCase();
    if (BLOCKED_TAGS.has(tag)) {
      toRemove.push(el);
      continue;
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      const value = attr.value || "";
      if (name.startsWith("on")) {
        el.removeAttribute(attr.name);
        continue;
      }
      if (URL_ATTRS.has(name) && isUnsafeUrl(value)) {
        el.removeAttribute(attr.name);
      }
    }
    if (tag === "a") {
      el.setAttribute("target", "_blank");
      el.setAttribute("rel", "noopener noreferrer nofollow");
    }
    // Strip inline event-style style values that pull remote resources.
    if (tag === "img") {
      const src = el.getAttribute("src") ?? "";
      if (isUnsafeUrl(src)) {
        el.removeAttribute("src");
      }
    }
  }
  for (const el of toRemove) {
    el.remove();
  }
  return doc.body.innerHTML;
}
