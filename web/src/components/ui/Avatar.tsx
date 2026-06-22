import Image from 'next/image';

import { cn, initials } from '@/lib/utils';

export interface AvatarProps {
  name: string;
  src?: string | null;
  size?: number;
  className?: string;
}

export function Avatar({ name, src, size = 36, className }: AvatarProps) {
  const dimension = { width: size, height: size };
  if (src) {
    return (
      <Image
        src={src}
        alt={name}
        {...dimension}
        className={cn('rounded-full object-cover', className)}
      />
    );
  }
  return (
    <span
      style={dimension}
      className={cn(
        'inline-flex select-none items-center justify-center rounded-full bg-brand-100 text-xs font-semibold text-brand-700',
        className,
      )}
      aria-label={name}
    >
      {initials(name)}
    </span>
  );
}
