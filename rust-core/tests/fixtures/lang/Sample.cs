using System;
using Alias = System.Text.Encoding;

namespace App.Core
{
    /// <summary>Doc comment.</summary>
    public class Widget : Base, IThing
    {
        // COMMENT_ONLY_WORD
        private int count;

        public string Name { get; set; }

        public Widget(int c) { count = c; }

        public int Compute(int a, int b)
        {
            var s = "STRING_ONLY_WORD";
            return Helper(a) + b;
        }

        void Hidden() { }
    }

    interface IThing
    {
        void Run();
    }

    public struct Point { }

    enum Mode { Fast }
}
