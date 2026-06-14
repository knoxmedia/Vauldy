type Props = {
  className?: string;
  size?: number;
};

export default function VerticalMoreIcon({ className, size = 32 }: Props) {
  return (
    <svg
      aria-hidden="true"
      className={className}
      fill="currentColor"
      height={size}
      viewBox="0 0 48 48"
      width={size}
      xmlns="http://www.w3.org/2000/svg"
    >
      <path d="M24 15C25.6569 15 27 13.6569 27 12C27 10.3431 25.6569 9 24 9C22.3431 9 21 10.3431 21 12C21 13.6569 22.3431 15 24 15Z" />
      <path d="M24 27C25.6569 27 27 25.6569 27 24C27 22.3431 25.6569 21 24 21C22.3431 21 21 22.3431 21 24C21 25.6569 22.3431 27 24 27Z" />
      <path d="M27 36C27 37.6569 25.6569 39 24 39C22.3431 39 21 37.6569 21 36C21 34.3431 22.3431 33 24 33C25.6569 33 27 34.3431 27 36Z" />
    </svg>
  );
}
