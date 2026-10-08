using System.Text;
using System.Xml;

namespace Plaitway.Localization;

/// <summary>Writes the strings of one language as a .resw file, the resource format of WinUI.</summary>
internal static class ReswWriter
{
    private const string MimeType = "text/microsoft-resx";
    private const string SchemaVersion = "2.0";
    private const string ReaderType = "System.Resources.ResXResourceReader, System.Windows.Forms, Version=4.0.0.0, Culture=neutral, PublicKeyToken=b77a5c561934e089";
    private const string WriterType = "System.Resources.ResXResourceWriter, System.Windows.Forms, Version=4.0.0.0, Culture=neutral, PublicKeyToken=b77a5c561934e089";

    /// <summary>The file for <paramref name="language"/>.</summary>
    public static string Render(IEnumerable<LocalizedString> strings, Language language)
    {
        var output = new StringBuilder();
        var settings = new XmlWriterSettings { Indent = true, IndentChars = "  ", NewLineChars = "\n" };
        using (var writer = XmlWriter.Create(output, settings))
        {
            writer.WriteStartDocument();
            writer.WriteStartElement("root");
            WriteHeader(writer, "resmimetype", MimeType);
            WriteHeader(writer, "version", SchemaVersion);
            WriteHeader(writer, "reader", ReaderType);
            WriteHeader(writer, "writer", WriterType);
            foreach (var text in strings)
            {
                WriteData(writer, text.Id, text.Formats[language.Tag]);
            }

            writer.WriteEndElement();
            writer.WriteEndDocument();
        }

        return output.ToString();
    }

    private static void WriteHeader(XmlWriter writer, string name, string value)
    {
        writer.WriteStartElement("resheader");
        writer.WriteAttributeString("name", name);
        writer.WriteElementString("value", value);
        writer.WriteEndElement();
    }

    private static void WriteData(XmlWriter writer, string name, string value)
    {
        writer.WriteStartElement("data");
        writer.WriteAttributeString("name", name);
        writer.WriteAttributeString("xml", "space", null, "preserve");
        writer.WriteElementString("value", value);
        writer.WriteEndElement();
    }
}
