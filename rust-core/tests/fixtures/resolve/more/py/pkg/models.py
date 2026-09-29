from .util import format_value


class Base:
    def greet(self):
        return format_value(1)

    def shout(self):
        return self.greet().upper()


class Child(Base):
    def run(self):
        return self.shout() + self.greet()
