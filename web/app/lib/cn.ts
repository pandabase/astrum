import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** Joins class names, letting later Tailwind classes override earlier ones, so callers can adjust a component's base style. */
export function cn(...classes: ClassValue[]) {
  return twMerge(clsx(classes));
}
