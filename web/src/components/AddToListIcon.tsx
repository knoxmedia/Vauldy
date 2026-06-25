import type { SVGProps } from "react";

/** 添加到列表（加号 + 列表线，currentColor） */
export default function AddToListIcon(props: SVGProps<SVGSVGElement>) {
  const { className, ...rest } = props;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 48 48"
      fill="currentColor"
      className={className}
      aria-hidden
      {...rest}
    >
      <path d="M11 13H17V16H11V22H8V16H2V13H8V7H11V13Z" fill="currentColor" />
      <path d="M42 9H21V12H42V9Z" fill="currentColor" />
      <path d="M42 18H21V21H42V18Z" fill="currentColor" />
      <path d="M10.5 27H42V30H10.5V27Z" fill="currentColor" />
      <path d="M42 36H10.5V39H42V36Z" fill="currentColor" />
    </svg>
  );
}
