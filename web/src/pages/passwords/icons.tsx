// Shared item-type icon mapping and favicon rendering used by the list and
// detail views.

import { useState } from "react";
import {
  CreditCard,
  Globe,
  StickyNote,
  UserRound,
  type LucideIcon,
} from "lucide-react";

import type { VaultItemType } from "@/lib/vault/api";

export function itemIcon(type: VaultItemType): LucideIcon {
  switch (type) {
    case "login":
      return Globe;
    case "card":
      return CreditCard;
    case "identity":
      return UserRound;
    default:
      return StickyNote;
  }
}

// extractDomain parses a user-entered URL into a bare hostname, returning null
// when the value is empty or not a valid URL. Used to drive the favicon lookup
// and to guard against passing arbitrary strings to the icon proxy.
export function extractDomain(url: string): string | null {
  if (!url) return null;
  try {
    const u = new URL(url.startsWith("http") ? url : `https://${url}`);
    const h = u.hostname;
    return h || null;
  } catch {
    return null;
  }
}

// Favicon loads a site icon through the authenticated /api/vault/icon proxy so
// the request stays same-origin. Falls back silently (renders nothing) on
// error; the caller is expected to show a type icon as the default.
export function Favicon({
  domain,
  size = 16,
}: {
  domain: string;
  size?: number;
}) {
  const [failed, setFailed] = useState(false);
  if (failed) return null;
  return (
    <img
      src={`/api/vault/icon?domain=${encodeURIComponent(domain)}`}
      alt=""
      width={size}
      height={size}
      loading="lazy"
      className="rounded-sm"
      onError={() => setFailed(true)}
    />
  );
}
