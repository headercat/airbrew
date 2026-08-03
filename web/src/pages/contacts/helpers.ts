import { isApiError } from "@/lib/api";
import type {
  ContactAddress,
  ContactPayload,
  ContactRecord,
} from "@/lib/contacts";

// Form value rows carry a stable client-side id so React list keys survive
// add/remove/reorder (a plain array index would reuse the wrong DOM node and
// shuffle typed values between rows). The id is stripped before submit.
export type FormValue = { id: string; value: string; type?: string };
export type FormAddress = { id: string } & ContactAddress;

function rid(): string {
  // crypto.randomUUID is available in all evergreen browsers and the embedded
  // SPA runs on a controlled runtime.
  return (globalThis.crypto?.randomUUID?.() ??
    Math.random().toString(36).slice(2)) as string;
}

export function newFormValue(value = "", type = ""): FormValue {
  return { id: rid(), value, type };
}

export function newFormAddress(): FormAddress {
  return { id: rid() };
}

export type ContactFormState = {
  namePrefix: string;
  givenName: string;
  middleName: string;
  familyName: string;
  nameSuffix: string;
  displayName: string;
  nickname: string;
  company: string;
  title: string;
  department: string;
  emails: FormValue[];
  phones: FormValue[];
  addresses: FormAddress[];
  ims: FormValue[];
  urls: FormValue[];
  birthday: string;
  notes: string;
  isFavorite: boolean;
  groupIDs: string[];
};

export const blankForm: ContactFormState = {
  namePrefix: "",
  givenName: "",
  middleName: "",
  familyName: "",
  nameSuffix: "",
  displayName: "",
  nickname: "",
  company: "",
  title: "",
  department: "",
  emails: [newFormValue("", "work")],
  phones: [newFormValue("", "mobile")],
  addresses: [],
  ims: [],
  urls: [],
  birthday: "",
  notes: "",
  isFavorite: false,
  groupIDs: [],
};

export function contactToForm(contact: ContactRecord | null): ContactFormState {
  if (!contact?.id) return cloneBlank();
  return {
    namePrefix: contact.name_prefix,
    givenName: contact.given_name,
    middleName: contact.middle_name,
    familyName: contact.family_name,
    nameSuffix: contact.name_suffix,
    displayName: contact.display_name,
    nickname: contact.nickname,
    company: contact.company,
    title: contact.title,
    department: contact.department,
    emails: toFormValues(contact.emails, "work"),
    phones: toFormValues(contact.phones, "mobile"),
    addresses: (contact.addresses.length ? contact.addresses : []).map((a) => ({
      id: rid(),
      type: a.type,
      street: a.street ?? "",
      locality: a.locality ?? "",
      region: a.region ?? "",
      postal_code: a.postal_code ?? "",
      country: a.country ?? "",
    })),
    ims: toFormValues(contact.ims),
    urls: toFormValues(contact.urls),
    birthday: contact.birthday ?? "",
    notes: contact.notes,
    isFavorite: contact.is_favorite,
    groupIDs: contact.group_ids ?? [],
  };
}

function toFormValues(
  values: { value: string; type?: string }[],
  defaultType = "",
): FormValue[] {
  const out = values.map((v) => ({
    id: rid(),
    value: v.value,
    type: v.type ?? defaultType,
  }));
  return out.length ? out : [newFormValue("", defaultType)];
}

export function formToPayload(form: ContactFormState): ContactPayload {
  return {
    name_prefix: form.namePrefix,
    given_name: form.givenName,
    middle_name: form.middleName,
    family_name: form.familyName,
    name_suffix: form.nameSuffix,
    display_name: form.displayName,
    nickname: form.nickname,
    company: form.company,
    title: form.title,
    department: form.department,
    emails: cleanValues(form.emails),
    phones: cleanValues(form.phones),
    addresses: cleanAddresses(form.addresses),
    ims: cleanValues(form.ims),
    urls: cleanValues(form.urls),
    birthday: form.birthday,
    notes: form.notes,
    is_favorite: form.isFavorite,
    group_ids: form.groupIDs,
  };
}

function cleanValues(values: FormValue[]) {
  return values
    .map((item) => ({
      value: item.value.trim(),
      type: item.type?.trim() || undefined,
    }))
    .filter((item) => item.value);
}

function cleanAddresses(values: FormAddress[]): ContactAddress[] {
  return values
    .map((a) => ({
      type: a.type?.trim() || undefined,
      street: a.street?.trim() ?? "",
      locality: a.locality?.trim() ?? "",
      region: a.region?.trim() ?? "",
      postal_code: a.postal_code?.trim() ?? "",
      country: a.country?.trim() ?? "",
    }))
    .filter(
      (a) => a.street || a.locality || a.region || a.postal_code || a.country,
    );
}

function cloneBlank(): ContactFormState {
  // Return a fresh copy of blankForm with newly-generated value row ids so
  // each editor gets its own keys. Do NOT recurse through contactToForm
  // (emptyContact().id is "" which would re-trigger the blank path forever).
  return {
    ...blankForm,
    emails: [newFormValue("", "work")],
    phones: [newFormValue("", "mobile")],
    addresses: [],
    ims: [],
    urls: [],
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

export function hasIdentity(form: ContactFormState): boolean {
  if (
    form.displayName.trim() ||
    form.givenName.trim() ||
    form.familyName.trim() ||
    form.middleName.trim() ||
    form.namePrefix.trim() ||
    form.nameSuffix.trim() ||
    form.nickname.trim() ||
    form.company.trim() ||
    form.title.trim() ||
    form.department.trim()
  ) {
    return true;
  }
  if (form.emails.some((e) => e.value.trim())) return true;
  if (form.phones.some((p) => p.value.trim())) return true;
  return false;
}

export function displayName(contact: ContactRecord) {
  return (
    contact.display_name ||
    [contact.given_name, contact.family_name].filter(Boolean).join(" ") ||
    contact.nickname ||
    contact.emails[0]?.value ||
    contact.phones[0]?.value ||
    ""
  );
}

export function subtitle(contact: ContactRecord) {
  return (
    [contact.company, contact.title].filter(Boolean).join(" · ") ||
    contact.emails[0]?.value ||
    contact.phones[0]?.value ||
    ""
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
  return e instanceof Error ? e.message : "";
}
