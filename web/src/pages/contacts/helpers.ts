import { isApiError } from "@/lib/api";
import type { ContactPayload, ContactRecord } from "@/lib/contacts";

export type ContactFormState = {
  displayName: string;
  givenName: string;
  familyName: string;
  nickname: string;
  company: string;
  title: string;
  department: string;
  email: string;
  emailType: string;
  phone: string;
  phoneType: string;
  birthday: string;
  notes: string;
  isFavorite: boolean;
  groupIDs: string[];
};

export const blankForm: ContactFormState = {
  displayName: "",
  givenName: "",
  familyName: "",
  nickname: "",
  company: "",
  title: "",
  department: "",
  email: "",
  emailType: "work",
  phone: "",
  phoneType: "mobile",
  birthday: "",
  notes: "",
  isFavorite: false,
  groupIDs: [],
};

export function contactToForm(contact: ContactRecord | null): ContactFormState {
  if (!contact?.id) return blankForm;
  return {
    displayName: contact.display_name,
    givenName: contact.given_name,
    familyName: contact.family_name,
    nickname: contact.nickname,
    company: contact.company,
    title: contact.title,
    department: contact.department,
    email: contact.emails[0]?.value ?? "",
    emailType: contact.emails[0]?.type ?? "work",
    phone: contact.phones[0]?.value ?? "",
    phoneType: contact.phones[0]?.type ?? "mobile",
    birthday: contact.birthday ?? "",
    notes: contact.notes,
    isFavorite: contact.is_favorite,
    groupIDs: contact.group_ids ?? [],
  };
}

export function formToPayload(form: ContactFormState): ContactPayload {
  return {
    display_name: form.displayName,
    given_name: form.givenName,
    family_name: form.familyName,
    nickname: form.nickname,
    company: form.company,
    title: form.title,
    department: form.department,
    emails: form.email ? [{ value: form.email, type: form.emailType }] : [],
    phones: form.phone ? [{ value: form.phone, type: form.phoneType }] : [],
    birthday: form.birthday,
    notes: form.notes,
    is_favorite: form.isFavorite,
    group_ids: form.groupIDs,
  };
}

export function emptyContact(): ContactRecord {
  return {
    id: "",
    name_prefix: "",
    given_name: "",
    middle_name: "",
    family_name: "",
    name_suffix: "",
    display_name: "",
    nickname: "",
    company: "",
    title: "",
    department: "",
    emails: [],
    phones: [],
    addresses: [],
    ims: [],
    urls: [],
    birthday: "",
    notes: "",
    is_favorite: false,
    group_ids: [],
    created_at: "",
    updated_at: "",
  };
}

export function displayName(contact: ContactRecord) {
  return (
    contact.display_name ||
    [contact.given_name, contact.family_name].filter(Boolean).join(" ") ||
    contact.nickname ||
    contact.emails[0]?.value ||
    contact.phones[0]?.value ||
    "이름 없음"
  );
}

export function subtitle(contact: ContactRecord) {
  return (
    [contact.company, contact.title].filter(Boolean).join(" · ") ||
    contact.emails[0]?.value ||
    contact.phones[0]?.value ||
    "세부 정보 없음"
  );
}

export function initials(contact: ContactRecord) {
  const name = displayName(contact);
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join("");
}

export function errorMessage(e: unknown) {
  if (isApiError(e)) return e.error_description ?? e.error;
  return e instanceof Error ? e.message : "요청을 처리하지 못했습니다.";
}
