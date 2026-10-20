import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatMetric(value: number | null | undefined, digits = 1) {
  if (value === null || value === undefined || !Number.isFinite(value)) return "Unavailable";
  return value.toLocaleString(undefined, { maximumFractionDigits: digits });
}

export function formatTime(unixMS: number) {
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit"
  }).format(new Date(unixMS));
}

export function shortID(id: string) {
  return id.length > 12 ? `${id.slice(0, 5)}…${id.slice(-5)}` : id;
}
