import { Circle, Square, sum } from './index';
import * as shapes from './shapes';

export function run(): number {
  const c = new Circle(1);
  c.grow();
  const sq: Square = new shapes.Square(2);
  const cube = new shapes.Cube(3);
  // helper() is only mentioned in this comment
  const note = "describe(c) in a string";
  return c.area() + sq.area() + cube.volume() + sum([c]) + helper() + note.length;
}

function helper(): number {
  const f = (x: number) => x;
  return f(1) + describe(new Circle(2));
}

function describe(s: shapes.Shape): number {
  return s.area();
}
