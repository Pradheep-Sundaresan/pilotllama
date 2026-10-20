import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-medium transition-all outline-none focus-visible:ring-2 focus-visible:ring-orange-400/60 disabled:pointer-events-none disabled:opacity-45",
  {
    variants: {
      variant: {
        default: "bg-[#df6235] text-stone-900 shadow-[0_8px_22px_rgba(196,77,36,.18)] hover:bg-[#c94f26]",
        secondary: "border border-orange-950/10 bg-white/75 text-stone-700 hover:bg-white",
        ghost: "text-stone-500 hover:bg-orange-950/[.05] hover:text-stone-900",
        danger: "border border-red-600/20 bg-red-50 text-red-700 hover:bg-red-100"
      },
      size: { default: "h-10 px-4", sm: "h-8 px-3 text-xs", icon: "size-9" }
    },
    defaultVariants: { variant: "default", size: "default" }
  }
);

export function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "button";
  return <Comp className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
