// Resolver precision fixture: classes, interfaces, re-exports, namespace imports.
export interface Shape {
  area(): number;
}

export class Circle implements Shape {
  constructor(public r: number) {}
  area(): number {
    return Math.PI * this.r * this.r;
  }
  grow(): void {
    this.r = scale(this.r);
  }
}

export class Square implements Shape {
  constructor(public s: number) {}
  area(): number {
    return this.s * this.s;
  }
}

export class Cube extends Square {
  volume(): number {
    return this.area() * this.s;
  }
}

function scale(x: number): number {
  return x * 2;
}

export function total(shapes: Shape[]): number {
  let t = 0;
  for (const s of shapes) {
    t += s.area();
  }
  return t;
}
