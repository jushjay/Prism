import React from 'react';

type TaijiMarkProps = {
  size?: number;
  className?: string;
};

export const TaijiMark: React.FC<TaijiMarkProps> = ({
  size = 40,
  className,
}) => (
  <svg
    aria-hidden="true"
    className={className}
    viewBox="0 0 64 64"
    width={size}
    height={size}
    fill="none"
    xmlns="http://www.w3.org/2000/svg"
  >
    <circle cx="32" cy="32" r="30" fill="#F7F1E8" />
    <path
      d="M32 2C48.5685 2 62 15.4315 62 32C62 48.5685 48.5685 62 32 62C40.8366 62 48 54.8366 48 46C48 37.1634 40.8366 30 32 30C23.1634 30 16 22.8366 16 14C16 5.16344 23.1634 2 32 2Z"
      fill="#161514"
    />
    <path
      d="M32 2C23.1634 2 16 9.16344 16 18C16 26.8366 23.1634 34 32 34C40.8366 34 48 41.1634 48 50C48 58.8366 40.8366 62 32 62C15.4315 62 2 48.5685 2 32C2 15.4315 15.4315 2 32 2Z"
      fill="#C65D3B"
    />
    <circle cx="32" cy="17.5" r="8.5" fill="#F7F1E8" />
    <circle cx="32" cy="46.5" r="8.5" fill="#161514" />
    <circle cx="32" cy="17.5" r="3.5" fill="#161514" />
    <circle cx="32" cy="46.5" r="3.5" fill="#F7F1E8" />
    <circle
      cx="32"
      cy="32"
      r="30"
      stroke="#161514"
      strokeOpacity="0.12"
      strokeWidth="2"
    />
  </svg>
);

type BrandLogoProps = {
  compact?: boolean;
};

const BrandLogo: React.FC<BrandLogoProps> = ({ compact = false }) => {
  if (compact) {
    return <TaijiMark size={32} />;
  }

  return (
    <div
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 12,
      }}
    >
      <TaijiMark size={44} />
      <div
        style={{
          lineHeight: 1,
        }}
      >
        <span
          style={{
            fontSize: 22,
            fontWeight: 700,
            letterSpacing: '0.08em',
            color: 'inherit',
          }}
        >
          PRISM
        </span>
      </div>
    </div>
  );
};

export default BrandLogo;
