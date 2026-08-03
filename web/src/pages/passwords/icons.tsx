// Shared item-type → icon mapping used by the list and detail views.

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
