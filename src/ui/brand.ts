import brandSvg from '../../assets/brand.svg?raw';

export const BRAND_SVG = brandSvg;

export function createBrandIcon(size = 40, className = 'brand-icon'): SVGSVGElement {
  const template = document.createElement('template');
  template.innerHTML = BRAND_SVG.trim();
  const icon = template.content.firstElementChild as SVGSVGElement;
  icon.setAttribute('width', String(size));
  icon.setAttribute('height', String(size));
  icon.setAttribute('class', className);
  icon.setAttribute('aria-hidden', 'true');
  return icon;
}

export function installFavicon(): void {
  if (document.getElementById('blive-brand-favicon')) return;
  const link = document.createElement('link');
  link.id = 'blive-brand-favicon';
  link.rel = 'icon';
  link.type = 'image/svg+xml';
  link.href = `data:image/svg+xml,${encodeURIComponent(BRAND_SVG)}`;
  document.head.append(link);
}
