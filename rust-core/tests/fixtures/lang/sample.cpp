#include <vector>

namespace geo {

// Doc comment.
class Shape : public Base {
 public:
  Shape(int side);
  virtual int area() const { return side_ * side_; }

 private:
  int side_;
};

int Shape::perimeter(int k) {
  // COMMENT_ONLY_WORD
  return area() * k;
}

template <typename T>
T twice(T v) {
  return v + v;
}

}  // namespace geo

using namespace std;

struct Plain {
  void run();
};
